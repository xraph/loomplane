package deployments

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/realtime"
)

type buildCredentialFunc func(context.Context, string, string) (*connections.Connection, []byte, error)

func (f buildCredentialFunc) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	return f(ctx, org, id)
}

type buildLogFixture struct {
	logs   []realtime.LogInput
	states []realtime.Collection
	fail   bool
}

func (f *buildLogFixture) AppendLog(_ context.Context, input realtime.LogInput, _ *realtime.Redactor) (*realtime.Log, error) {
	if f.fail {
		return nil, errors.New("log store unavailable")
	}
	f.logs = append(f.logs, input)
	return &realtime.Log{}, nil
}
func (f *buildLogFixture) SaveCheckpoint(_ context.Context, input realtime.LogInput, cursor, state, message string, _ *realtime.Redactor) error {
	f.states = append(f.states, realtime.Collection{OrgID: input.OrgID, ProjectID: input.ProjectID, DeploymentID: input.DeploymentID, ActionID: input.ActionID, Attempt: input.Attempt, OperationID: input.OperationID, Cursor: cursor, State: state, Message: message})
	return nil
}
func (*buildLogFixture) Checkpoint(context.Context, realtime.LogInput) (*realtime.Collection, error) {
	return nil, realtime.ErrNotFound
}

type buildRoundTripper func(*http.Request) (*http.Response, error)

func (f buildRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func imageBuildFixture(t *testing.T, push bool) (*Job, *ImageBuilder, *buildLogFixture, *int) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	config, err := server.ParseConfig(raw, map[string]string{"host": "builder.example.com", "user": "deploy", "host_key": string(ssh.MarshalAuthorizedKey(signer.PublicKey()))})
	if err != nil {
		t.Fatal(err)
	}
	config.Details["architecture"], config.Details["docker_id"] = "amd64", "build-fixture"
	connection := &connections.Connection{ID: "conn_builder", OrgID: "org_fixture", Cloud: connections.Server, Name: "Builder", Status: connections.StatusReady, Details: config.Details}
	var registryURL string
	registryAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			user, password, ok := r.BasicAuth()
			if !ok || user != "builder" || password != "registry-private-value" {
				t.Error("token service credential absent")
			}
			if !strings.HasPrefix(r.URL.Query().Get("scope"), "repository:team/loomplane/") {
				t.Errorf("wrong repository scope: %s", r.URL.Query().Get("scope"))
			}
			_, _ = io.WriteString(w, `{"token":"repository-scoped-token"}`)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("registry probe received a raw credential")
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+registryURL+`/token",service="fixture"`)
		w.WriteHeader(401)
	}))
	registryURL = registryAPI.URL
	t.Cleanup(registryAPI.Close)
	registry := &connections.Connection{ID: "conn_registry", OrgID: "org_fixture", Cloud: connections.Registry, Name: "Registry", Status: connections.StatusReady, Details: map[string]string{"endpoint": registryURL, "repository": "team", "api": "ready", "pull": "ready", "push": "ready"}}
	sink := &buildLogFixture{}
	job := &Job{Deployment: &Deployment{ID: "dep_fixture", OrgID: "org_fixture", ProjectID: "proj_fixture", EnvironmentID: "env_fixture"}, Action: planning.Action{ID: "service:api", Name: "api", Service: &planning.ServiceConfig{Source: "cmd/api", Image: "Dockerfile", Context: ".", BuildTarget: &planning.BuildTarget{Builder: connection.ID, BuilderSettings: planning.ReviewedConnectionSettings(connection.Cloud, connection.Details)}}}, SourceID: strings.Repeat("a", 40), Env: map[string]string{"RUNTIME_TOKEN": "runtime-private-value"}, Secrets: map[string]bool{"RUNTIME_TOKEN": true}, Logs: sink, Attempt: "1.1", Progress: func(string, string) {}}
	job.BuildCredentials = buildCredentialFunc(func(_ context.Context, org, id string) (*connections.Connection, []byte, error) {
		if org != "org_fixture" {
			return nil, nil, connections.ErrNotFound
		}
		if id == connection.ID {
			return connection, raw, nil
		}
		if id == registry.ID {
			return registry, []byte(`{"username":"builder","password":"registry-private-value"}`), nil
		}
		return nil, nil, connections.ErrNotFound
	})
	if push {
		job.Action.Service.BuildTarget.Registry = registry.ID
		job.Action.Service.BuildTarget.RegistrySettings = planning.ReviewedConnectionSettings(registry.Cloud, registry.Details)
	}
	archive := buildArchive(t, map[string]string{"Dockerfile": "FROM scratch\nCOPY marker /marker\n", "marker": "approved-source-marker"})
	job.Source = func(context.Context) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(archive)), int64(len(archive)), nil
	}
	builds := new(int)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"Version":"29.8.2","APIVersion":"1.54","Os":"linux","Arch":"amd64"}`)
		case r.URL.Path == "/info":
			_, _ = io.WriteString(w, `{"ID":"build-fixture","OSType":"linux","Architecture":"amd64"}`)
		case r.URL.Path == "/build":
			*builds++
			if r.Method != http.MethodPost || r.URL.Query().Get("version") != "2" || r.Header.Get("Content-Type") != "application/x-tar" {
				t.Errorf("build request: %s %s", r.Method, r.URL)
			}
			data, readErr := io.ReadAll(r.Body)
			if readErr != nil {
				t.Error(readErr)
			}
			files := contextFiles(t, data)
			if files["marker"] != "approved-source-marker" {
				t.Error("approved artifact marker absent")
			}
			if strings.Contains(string(data), "runtime-private-value") || strings.Contains(r.URL.RawQuery, "RUNTIME_TOKEN") || r.Header.Get("X-Registry-Auth") != "" {
				t.Error("runtime credentials entered build inputs")
			}
			_, _ = io.WriteString(w, "{\"stream\":\"same line runtime-private-value\\n\"}\n{\"stream\":\"same line runtime-private-value\\n\"}\n{\"id\":\"moby.image.id\",\"aux\":{\"ID\":\"sha256:"+strings.Repeat("b", 64)+"\"}}\n")
		case strings.HasPrefix(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:" + strings.Repeat("b", 64), "Config": map[string]any{"Labels": map[string]string{imageOwnerLabel: imageOwner(job), imageSourceLabel: job.SourceID}}})
		case strings.HasPrefix(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/push"):
			auth, decodeErr := base64.URLEncoding.DecodeString(r.Header.Get("X-Registry-Auth"))
			if decodeErr != nil || !strings.Contains(string(auth), "repository-scoped-token") || strings.Contains(string(auth), "registry-private-value") {
				t.Errorf("selected registry credential was not sent to push: %v", decodeErr)
			}
			_, _ = io.WriteString(w, "{\"status\":\"pushing\"}\n{\"aux\":{\"Digest\":\"sha256:"+strings.Repeat("c", 64)+"\"}}\n")
		default:
			t.Errorf("unexpected Docker API request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	imageBuilder := &ImageBuilder{Dial: func(_ context.Context, cfg *server.Config) (*server.Client, error) {
		return &server.Client{Config: cfg, HTTP: &http.Client{Transport: buildRoundTripper(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
			return http.DefaultTransport.RoundTrip(r)
		})}}, nil
	}}
	return job, imageBuilder, sink, builds
}

func TestImageBuild_remoteArtifactCheckpointAndRedactedRepeatedOutput(t *testing.T) {
	for _, push := range []bool{false, true} {
		t.Run(map[bool]string{false: "local image", true: "registry image"}[push], func(t *testing.T) {
			job, builder, sink, builds := imageBuildFixture(t, push)
			state := &ImageArtifact{}
			saves := 0
			image, err := builder.Build(t.Context(), job, state, func() error { saves++; return nil })
			if err != nil || image == "" || *builds != 1 || saves < 2 {
				t.Fatalf("remote build: %+v %d %d %v", state, *builds, saves, err)
			}
			if push && !strings.Contains(image, "/team/loomplane/") {
				t.Fatalf("published reference: %s", image)
			}
			if len(sink.logs) < 2 || sink.logs[0].Message != sink.logs[1].Message || sink.logs[0].ProviderID == sink.logs[1].ProviderID || strings.Contains(sink.logs[0].Message, "runtime-private-value") {
				t.Fatalf("retained output: %+v", sink.logs)
			}
			job.Source = func(context.Context) (io.ReadCloser, int64, error) {
				t.Fatal("checkpoint reopened the approved source")
				return nil, 0, nil
			}
			if _, err = builder.Build(t.Context(), job, state, func() error { return nil }); err != nil || *builds != 1 {
				t.Fatalf("checkpoint reuse: %d %v", *builds, err)
			}
		})
	}
}

func TestImageBuild_failedLogStorageDoesNotFailTheImage(t *testing.T) {
	job, builder, sink, _ := imageBuildFixture(t, false)
	sink.fail = true
	if _, err := builder.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if sink.states[len(sink.states)-1].State != "error" {
		t.Fatalf("missing collection failure: %+v", sink.states)
	}
}

func TestImageBuild_rejectsChangedBuilderAndUnsupportedSourceBeforeMutation(t *testing.T) {
	job, builder, _, builds := imageBuildFixture(t, false)
	job.Action.Service.BuildTarget.BuilderSettings["host"] = "changed.example.com"
	if _, err := builder.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil }); err == nil || *builds != 0 {
		t.Fatalf("unreviewed builder: %d %v", *builds, err)
	}
	job, builder, _, builds = imageBuildFixture(t, false)
	job.Action.Service.Image = "missing.Dockerfile"
	if _, err := builder.Build(t.Context(), job, &ImageArtifact{}, func() error { return nil }); err == nil || *builds != 0 {
		t.Fatalf("unsupported source mutated builder: %d %v", *builds, err)
	}
}

func TestImageBuild_retryRecordsANewAttemptAndMarksTheOldCollectionIncomplete(t *testing.T) {
	job, builder, sink, _ := imageBuildFixture(t, false)
	job.Attempt = "1.2"
	state := &ImageArtifact{Operation: "build_old", Attempt: "1.1"}
	if _, err := builder.Build(t.Context(), job, state, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if state.Attempt != "1.2" || state.Operation == "build_old" {
		t.Fatalf("retry reused old operation: %+v", state)
	}
	if len(sink.states) < 3 || sink.states[0].State != "incomplete" || sink.states[0].Attempt != "1.1" {
		t.Fatalf("old collection was not closed: %+v", sink.states)
	}
	for _, line := range sink.logs {
		if line.Attempt != "1.2" {
			t.Fatalf("new output belongs to old attempt: %+v", line)
		}
	}
}

func TestImageBuild_redactsChunkBoundariesBeforeTruncatingTheLine(t *testing.T) {
	job, _, sink, _ := imageBuildFixture(t, false)
	collector := job.Collector("build_fixture", job.LogAttempt())
	text := strings.Repeat("x", realtime.MaxLineBytes-5) + "runtime-private-value\n"
	first, err := json.Marshal(map[string]string{"stream": text[:realtime.MaxLineBytes]})
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(map[string]string{"stream": text[realtime.MaxLineBytes:]})
	if err != nil {
		t.Fatal(err)
	}
	_, err = readBuildOutput(bytes.NewReader(append(append(first, '\n'), append(second, '\n')...)), func(line buildLine) error { return collector.Emit(t.Context(), line) })
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.logs) != 1 || !sink.logs[0].Truncated || len(sink.logs[0].Message) > realtime.MaxLineBytes || strings.Contains(sink.logs[0].Message, "runti") {
		t.Fatalf("chunked secret or unbounded line: %+v", sink.logs)
	}
}

func TestImageBuildMutationsUsePhysicalFence(t *testing.T) {
	for _, path := range []string{"/build", "/images/team/image/push"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			client := &server.Client{RequireFence: true, HTTP: &http.Client{Transport: buildRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unfenced transport reached")
			})}}
			ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: "org/plan/service", Generation: 1})
			response, err := dockerRequest(ctx, client, http.MethodPost, path, nil, "", nil)
			if response != nil {
				response.Body.Close()
			}
			if err == nil || calls != 0 {
				t.Fatal("builder mutation bypassed SSH fence", calls, err)
			}
		})
	}
}
