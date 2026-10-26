//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.jetify.com/typeid/v2"
)

type liveLifecycleScope struct {
	Phase       string `json:"phase"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Plan        string `json:"plan,omitempty"`
	Route       string `json:"route_operation,omitempty"`
	Sync        string `json:"sync,omitempty"`
}

type liveLifecycleRead struct {
	Kind         string   `json:"kind"`
	Code         string   `json:"code"`
	HTTPStatus   int      `json:"http_status,omitempty"`
	Status       string   `json:"status,omitempty"`
	ErrorCode    string   `json:"error_code,omitempty"`
	Count        int      `json:"count"`
	Executable   bool     `json:"executable"`
	Steps        []string `json:"steps,omitempty"`
	ProblemCodes []string `json:"problem_codes,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
}

type liveLifecycleDiagnostic struct {
	Scope liveLifecycleScope  `json:"scope"`
	Reads []liveLifecycleRead `json:"reads"`
}

// Reads use the isolated authenticated application, with one shared deadline and
// a body limit. Only projections below reach disk; response errors never do.
func liveReadLifecycle(ctx context.Context, c *client, kind, path string) liveLifecycleRead {
	result := liveLifecycleRead{Kind: kind, Code: "read_unavailable"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, http.NoBody)
	if err != nil {
		return result
	}
	req.Header.Set("X-Publishable-Key", c.key)
	req.Header.Set("Authorization", "Bearer "+c.token)
	reader := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := reader.Do(req)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			result.Code = "read_denied"
		case http.StatusNotFound:
			result.Code = "not_found"
		default:
			result.Code = "http_error"
		}
		return result
	}
	var body struct {
		Status     string            `json:"status"`
		Error      string            `json:"error"`
		Message    string            `json:"message"`
		Executable bool              `json:"executable"`
		Actions    []json.RawMessage `json:"actions"`
		Problems   []struct {
			Code string `json:"code"`
		} `json:"problems"`
		Bindings []json.RawMessage `json:"bindings"`
		Steps    []struct {
			Status string `json:"status"`
		} `json:"steps"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 256*1024+1))
	if err != nil || len(raw) > 256*1024 || json.Unmarshal(raw, &body) != nil {
		return result
	}
	result.Code = "captured"
	result.Status = liveTerminalStatus(body.Status)
	result.ErrorCode = "none"
	if body.Error != "" || body.Message != "" {
		result.ErrorCode = "unknown"
	}
	result.Executable = body.Executable
	switch kind {
	case "plan":
		result.Count = len(body.Actions)
		if len(body.Problems) > 0 {
			result.ErrorCode = "problems_present"
			for _, problem := range body.Problems[:min(16, len(body.Problems))] {
				result.ProblemCodes = append(result.ProblemCodes, livePlanProblemCode(problem.Code))
			}
		}
	case "bindings":
		result.Count = len(body.Bindings)
	}
	result.Truncated = len(body.Steps) > 16 || len(body.Problems) > 16
	for _, step := range body.Steps[:min(16, len(body.Steps))] {
		result.Steps = append(result.Steps, liveTerminalStatus(step.Status))
	}
	return result
}

func liveDiagnosticID(value string) string {
	id, err := typeid.Parse(value)
	if err != nil {
		return "unknown"
	}
	switch id.Prefix() {
	case "proj", "env", "plan", "routeop", "sync", "csync":
		return value
	}
	return "unknown"
}

func liveCaptureLifecycle(path string, progress *liveProgress, c *client, scopes []liveLifecycleScope) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot := struct {
		Stage      string                    `json:"stage"`
		Code       string                    `json:"code"`
		HTTPStatus int                       `json:"http_status,omitempty"`
		Projects   []liveLifecycleDiagnostic `json:"projects"`
	}{Stage: progress.Stage, Code: progress.Code, HTTPStatus: progress.HTTPStatus, Projects: []liveLifecycleDiagnostic{}}
	if !liveStageAllowed(snapshot.Stage) {
		snapshot.Stage = "unknown"
	}
	if !liveFailureAllowed(snapshot.Code) {
		snapshot.Code = "unknown"
	}
	if snapshot.HTTPStatus < 100 || snapshot.HTTPStatus > 599 {
		snapshot.HTTPStatus = 0
	}
	for _, scope := range scopes[:min(3, len(scopes))] {
		safeScope := liveLifecycleScope{Project: liveDiagnosticID(scope.Project), Environment: liveDiagnosticID(scope.Environment)}
		switch scope.Phase {
		case "initial_release", "migration", "future_release":
			safeScope.Phase = scope.Phase
		default:
			safeScope.Phase = "unknown"
		}
		if scope.Plan != "" {
			safeScope.Plan = liveDiagnosticID(scope.Plan)
		}
		if scope.Route != "" {
			safeScope.Route = liveDiagnosticID(scope.Route)
		}
		if scope.Sync != "" {
			safeScope.Sync = liveDiagnosticID(scope.Sync)
		}
		diagnostic := liveLifecycleDiagnostic{Scope: safeScope, Reads: []liveLifecycleRead{}}
		if safeScope.Project == "unknown" {
			snapshot.Projects = append(snapshot.Projects, diagnostic)
			continue
		}
		base := "/api/v1/projects/" + scope.Project
		paths := []struct{ kind, path string }{{"bindings", base + "/bindings?environment=production"}}
		if safeScope.Plan != "" && safeScope.Plan != "unknown" {
			paths = append(paths, struct{ kind, path string }{"plan", "/api/v1/plans/" + scope.Plan}, struct{ kind, path string }{"deployment", "/api/v1/plans/" + scope.Plan + "/deployment"})
		}
		if safeScope.Route != "" && safeScope.Route != "unknown" {
			paths = append(paths, struct{ kind, path string }{"route", base + "/route-operations/" + scope.Route})
		}
		if safeScope.Sync != "" && safeScope.Sync != "unknown" {
			paths = append(paths, struct{ kind, path string }{"sync", base + "/cloud-syncs/" + scope.Sync})
		}
		for _, read := range paths {
			diagnostic.Reads = append(diagnostic.Reads, liveReadLifecycle(ctx, c, read.kind, read.path))
		}
		snapshot.Projects = append(snapshot.Projects, diagnostic)
	}
	file := filepath.Join(filepath.Dir(path), "run-lifecycle.json")
	reservation, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = reservation.Close(); err != nil {
		return err
	}
	return liveWrite(file, snapshot)
}

func liveLifecycleDiagnostics(t *testing.T, progress *liveProgress, c *client, projects []string) ([]liveLifecycleScope, func()) {
	t.Helper()
	scopes := make([]liveLifecycleScope, len(projects))
	for index, project := range projects {
		scopes[index] = liveLifecycleScope{Project: project, Phase: "initial_release"}
	}
	return scopes, func() {
		if err := liveCaptureLifecycle(progress.path, progress, c, scopes); err != nil {
			t.Log("lifecycle snapshot unavailable; preserve run progress")
		}
	}
}

func livePlanProblemCode(value string) string {
	switch value {
	case "pool-target-required", "capacity-architecture", "cloud-target", "cloud-target-unresolved", "cloud-server-ambiguous", "cloud-tag-unmatched", "cloud-tag-ambiguous", "not-deployable", "no-provider", "unknown-provider", "connection-gone", "connection-not-ready", "no-connection", "connection-ambiguous":
		return value
	default:
		return "unknown"
	}
}
