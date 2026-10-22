package azure

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	cloudprogress "github.com/xraph/loomplane/internal/clouds/progress"
)

// API versions.
const (
	registryVersion = "2023-07-01"
	// ACR Tasks' source upload and runs are only in this version.
	tasksVersion = "2019-06-01-preview"
	appsVersion  = "2024-03-01"
)

// buildTimeout bounds an ACR Tasks run, in seconds.
const buildTimeout = 3600

func progress(ctx context.Context, message string) { cloudprogress.Report(ctx, message) }

// --- Resource groups and resource providers ---

// EnsureGroup makes the session's resource group in location, unless
// it's there.
func (s *Session) EnsureGroup(ctx context.Context, location string) error {
	exists, err := s.groupExists(ctx)
	if err != nil || exists {
		return err
	}
	_, err = s.do(ctx, http.MethodPut, s.group(), resourcesVersion, map[string]any{"location": location}, nil)
	return err
}

// EnsureRegistered registers each of Namespaces the subscription hasn't,
// and waits until they are.
func (s *Session) EnsureRegistered(ctx context.Context) error {
	for _, ns := range Namespaces {
		ok, err := s.registered(ctx, ns)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, rerr := s.do(ctx, http.MethodPost, s.subscription()+"/providers/"+ns+"/register", resourcesVersion, nil, nil); rerr != nil {
			return fmt.Errorf("register %s in the subscription: %w", ns, rerr)
		}
		for !ok {
			progress(ctx, "Registering "+ns+" in the subscription")
			if werr := s.wait(ctx); werr != nil {
				return werr
			}
			if ok, err = s.registered(ctx, ns); err != nil {
				return err
			}
		}
	}
	return nil
}

// provisioned waits for the resource at path to finish provisioning, and
// fails if it didn't succeed. state reads the resource's state from what
// GET returns into out.
func (s *Session) provisioned(ctx context.Context, path, version, what string, out any, state func() string) error {
	for {
		if _, err := s.do(ctx, http.MethodGet, path, version, nil, out); err != nil {
			return err
		}
		switch st := state(); strings.ToLower(st) {
		case "succeeded":
			return nil
		case "failed", "canceled", "cancelled", "upgradefailed":
			return fmt.Errorf("azure couldn't make %s: provisioning %s", what, strings.ToLower(st))
		default:
			progress(ctx, "Waiting for Azure to make "+what+" ("+st+")")
		}
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

// --- Container Registry ---

// Registry is a container registry.
type Registry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Properties struct {
		LoginServer       string `json:"loginServer"`
		ProvisioningState string `json:"provisioningState"`
	} `json:"properties"`
}

// RegistryName is the registry Loomplane builds into for a subscription's
// resource group. Registry names are global across Azure, so it's a hash
// of both.
func RegistryName(subscription, group string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(subscription + "/" + group)))
	return "loomplane" + hex.EncodeToString(sum[:])[:16]
}

func (s *Session) registryPath() string {
	return s.group() + "/providers/Microsoft.ContainerRegistry/registries/" + RegistryName(s.cred.SubscriptionID, s.cred.ResourceGroup)
}

// EnsureRegistry makes the resource group's registry in location, unless
// it's there, and waits until it can be built into. Container Apps pulls
// with its admin user unless the principal may give apps a pull
// identity.
func (s *Session) EnsureRegistry(ctx context.Context, location string) (*Registry, error) {
	var r Registry
	_, err := s.do(ctx, http.MethodGet, s.registryPath(), registryVersion, nil, &r)
	if err == nil && strings.EqualFold(r.Properties.ProvisioningState, "Succeeded") {
		return &r, nil
	}
	if err != nil && !IsNotFound(err) {
		return nil, err
	}
	if IsNotFound(err) {
		body := map[string]any{
			"location":   location,
			"sku":        map[string]string{"name": "Basic"},
			"tags":       map[string]string{"managed-by": "loomplane"},
			"properties": map[string]any{"adminUserEnabled": true},
		}
		if _, err := s.do(ctx, http.MethodPut, s.registryPath(), registryVersion, body, &r); err != nil {
			return nil, fmt.Errorf("make the container registry: %w", err)
		}
	}
	if err := s.provisioned(ctx, s.registryPath(), registryVersion, "the container registry", &r, func() string { return r.Properties.ProvisioningState }); err != nil {
		return nil, err
	}
	return &r, nil
}

// RegistryLogin is the registry's admin user.
type RegistryLogin struct {
	Username, Password string
}

// RegistryLogin reads the registry's admin user.
func (s *Session) RegistryLogin(ctx context.Context) (*RegistryLogin, error) {
	var out struct {
		Username  string `json:"username"`
		Passwords []struct {
			Value string `json:"value"`
		} `json:"passwords"`
	}
	if _, err := s.do(ctx, http.MethodPost, s.registryPath()+"/listCredentials", registryVersion, nil, &out); err != nil {
		return nil, err
	}
	if out.Username == "" || len(out.Passwords) == 0 || out.Passwords[0].Value == "" {
		return nil, errors.New("the container registry has no admin user to pull with")
	}
	return &RegistryLogin{Username: out.Username, Password: out.Passwords[0].Value}, nil
}

// UploadSource uploads a gzipped tarball for ACR Tasks to build from, and
// returns where the build finds it.
func (s *Session) UploadSource(ctx context.Context, src io.Reader, size int64) (string, error) {
	var up struct {
		UploadURL    string `json:"uploadUrl"`
		RelativePath string `json:"relativePath"`
	}
	if _, err := s.do(ctx, http.MethodPost, s.registryPath()+"/listBuildSourceUploadUrl", tasksVersion, nil, &up); err != nil {
		return "", err
	}
	if up.UploadURL == "" || up.RelativePath == "" {
		return "", errors.New("the container registry gave no place to upload the source")
	}
	// The upload URL carries its own signature; it takes no token.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, up.UploadURL, src)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("Content-Type", "application/gzip")
	res, err := s.c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload the source: %w", err)
	}
	defer res.Body.Close() // read-only
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10)) //nolint:errcheck // only for the message
		return "", fmt.Errorf("upload the source: azure storage answered %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return up.RelativePath, nil
}

// BuildRequest is a build ACR Tasks runs on uploaded source: a Docker
// build, or a buildpack build when Pack is set.
type BuildRequest struct {
	// Source is where UploadSource put the source.
	Source string
	// Image is the repository and tag to push, without the registry.
	Image string
	// Dockerfile and Context are paths in the source. Pack replaces them.
	Dockerfile, Context string
	// Pack builds with buildpacks instead of a Dockerfile.
	Pack *PackBuild
}

// PackBuild is a build by the pack CLI, for a service that has no
// Dockerfile.
type PackBuild struct {
	// Image is the container image that has pack in it, and Builder the
	// buildpack builder it uses.
	Image, Builder string
	// Path is the directory in the source to build.
	Path string
	// Env are KEY=VALUE variables for the builder.
	Env []string
}

// unsafeInTask is what a value spliced into a task file may not hold.
const unsafeInTask = " \t\r\n\"'`$\\{}"

// task is the ACR Tasks file for a build: docker build or pack build,
// then push.
func (b BuildRequest) task() (string, error) {
	ref := "{{.Run.Registry}}/" + b.Image
	if b.Pack != nil {
		return b.packTask(ref)
	}
	for _, v := range []string{b.Image, b.Dockerfile, b.Context} {
		if v == "" || strings.ContainsAny(v, unsafeInTask) {
			return "", fmt.Errorf("ACR Tasks can't build from %q", v)
		}
	}
	return "version: v1.1.0\nsteps:\n" +
		"  - build: -t " + ref + " -f " + b.Dockerfile + " " + b.Context + "\n" +
		"  - push:\n    - \"" + ref + "\"\n", nil
}

// packTask is the task file for a buildpack build. The command line gets
// each variable's name and the step's env its value, quoted, so a value
// with spaces in it (a start command) is never split.
func (b BuildRequest) packTask(ref string) (string, error) {
	p := b.Pack
	for _, v := range []string{b.Image, p.Image, p.Builder, p.Path} {
		if v == "" || strings.ContainsAny(v, unsafeInTask) {
			return "", fmt.Errorf("ACR Tasks can't build from %q", v)
		}
	}
	cmd := p.Image + " build " + ref + " --builder " + p.Builder + " --path " + p.Path
	var env []string
	for _, e := range p.Env {
		name, value, ok := strings.Cut(e, "=")
		if !ok || name == "" || strings.ContainsAny(name, unsafeInTask) || strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("ACR Tasks can't build with the variable %q", name)
		}
		cmd += " --env " + name
		env = append(env, strconv.Quote(e))
	}
	step := "  - cmd: " + cmd + "\n    entryPoint: pack\n"
	if len(env) > 0 {
		step += "    env: [" + strings.Join(env, ", ") + "]\n"
	}
	return "version: v1.1.0\nsteps:\n" + step +
		"  - push:\n    - \"" + ref + "\"\n", nil
}

// Run is an ACR Tasks run.
type Run struct {
	Properties struct {
		RunID           string `json:"runId"`
		Status          string `json:"status"`
		RunErrorMessage string `json:"runErrorMessage"`
	} `json:"properties"`
}

// Done reports whether the run has finished, either way.
func (r *Run) Done() bool {
	switch r.Properties.Status {
	case "Succeeded", "Failed", "Canceled", "Error", "Timeout":
		return true
	}
	return false
}

// StartBuild starts a build on ACR Tasks and returns its run ID.
func (s *Session) StartBuild(ctx context.Context, b BuildRequest) (string, error) {
	task, err := b.task()
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"type":               "EncodedTaskRunRequest",
		"encodedTaskContent": base64.StdEncoding.EncodeToString([]byte(task)),
		"sourceLocation":     b.Source,
		"platform":           map[string]string{"os": "Linux", "architecture": "amd64"},
		"timeout":            buildTimeout,
	}
	var run Run
	header, err := s.do(ctx, http.MethodPost, s.registryPath()+"/scheduleRun", tasksVersion, body, &run)
	if err != nil {
		return "", fmt.Errorf("start the build: %w", err)
	}
	// Scheduling can be a long-running operation that answers 202 and
	// points at where the run will be.
	for run.Properties.RunID == "" {
		next := header.Get("Azure-AsyncOperation")
		if next == "" {
			next = header.Get("Location")
		}
		if next == "" {
			return "", errors.New("ACR Tasks accepted the build without a run ID")
		}
		if werr := s.wait(ctx); werr != nil {
			return "", werr
		}
		if header, err = s.do(ctx, http.MethodGet, next, "", nil, &run); err != nil {
			return "", fmt.Errorf("start the build: %w", err)
		}
	}
	return run.Properties.RunID, nil
}

// WaitBuild polls a run until it finishes, and fails unless it
// succeeded.
func (s *Session) WaitBuild(ctx context.Context, runID string) error {
	path := s.registryPath() + "/runs/" + url.PathEscape(runID)
	for {
		var run Run
		if _, err := s.do(ctx, http.MethodGet, path, tasksVersion, nil, &run); err != nil {
			return err
		}
		if run.Done() {
			if run.Properties.Status == "Succeeded" {
				return nil
			}
			detail := strings.TrimSpace(run.Properties.RunErrorMessage)
			if detail == "" {
				detail = strings.ToLower(run.Properties.Status)
			}
			return fmt.Errorf("ACR Tasks run %s: %s. Its log is under Services, Tasks, Runs in the container registry %s", runID, detail, RegistryName(s.cred.SubscriptionID, s.cred.ResourceGroup))
		}
		progress(ctx, "Building on ACR Tasks ("+strings.ToLower(run.Properties.Status)+")")
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

// --- Container Apps ---

// EnvironmentName is the Container Apps environment Loomplane runs apps
// in, one per region.
func EnvironmentName(location string) string { return "loomplane-" + strings.ToLower(location) }

// Environment is a Container Apps environment.
type Environment struct {
	ID         string `json:"id"`
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
		DefaultDomain     string `json:"defaultDomain"`
	} `json:"properties"`
}

// EnsureEnvironment makes the region's Container Apps environment,
// unless it's there, and waits until apps can join it. It's consumption
// only, with no Log Analytics workspace.
func (s *Session) EnsureEnvironment(ctx context.Context, location string) (*Environment, error) {
	path := s.group() + "/providers/Microsoft.App/managedEnvironments/" + EnvironmentName(location)
	var env Environment
	_, err := s.do(ctx, http.MethodGet, path, appsVersion, nil, &env)
	if err == nil && strings.EqualFold(env.Properties.ProvisioningState, "Succeeded") {
		return &env, nil
	}
	if err != nil && !IsNotFound(err) {
		return nil, err
	}
	if IsNotFound(err) {
		body := map[string]any{
			"location":   location,
			"tags":       map[string]string{"managed-by": "loomplane"},
			"properties": map[string]any{},
		}
		if _, err := s.do(ctx, http.MethodPut, path, appsVersion, body, &env); err != nil {
			return nil, fmt.Errorf("make the container apps environment: %w", err)
		}
	}
	if err := s.provisioned(ctx, path, appsVersion, "the container apps environment", &env, func() string { return env.Properties.ProvisioningState }); err != nil {
		return nil, err
	}
	return &env, nil
}

// App is the part of a container app Loomplane sets and reads.
type App struct {
	ID         string            `json:"id,omitempty"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   *ManagedIdentity  `json:"identity,omitempty"`
	Properties AppProperties     `json:"properties"`
}

// ManagedIdentity is what an app may act as: Type "UserAssigned" with
// the identities' resource IDs as keys.
type ManagedIdentity struct {
	Type                   string              `json:"type"`
	UserAssignedIdentities map[string]struct{} `json:"userAssignedIdentities,omitempty"`
}

// AppProperties are a container app's.
type AppProperties struct {
	EnvironmentID string        `json:"managedEnvironmentId"`
	Configuration Configuration `json:"configuration"`
	Template      Template      `json:"template"`
	// Read only.
	ProvisioningState   string `json:"provisioningState,omitempty"`
	LatestRevisionName  string `json:"latestRevisionName,omitempty"`
	LatestReadyRevision string `json:"latestReadyRevisionName,omitempty"`
}

// Configuration is how an app is reached and what it may read.
type Configuration struct {
	ActiveRevisionsMode string        `json:"activeRevisionsMode,omitempty"`
	Ingress             *Ingress      `json:"ingress,omitempty"`
	Registries          []AppRegistry `json:"registries,omitempty"`
	Secrets             []Secret      `json:"secrets,omitempty"`
}

// Ingress is how an app takes HTTP traffic: from anywhere when External,
// else only from apps in its environment.
type Ingress struct {
	External   bool   `json:"external"`
	TargetPort int    `json:"targetPort"`
	Transport  string `json:"transport,omitempty"`
	// Read only.
	FQDN string `json:"fqdn,omitempty"`
}

// AppRegistry is a registry an app pulls from: as Identity, a managed
// identity's resource ID, or as Username with the password in an app
// secret.
type AppRegistry struct {
	Server            string `json:"server"`
	Identity          string `json:"identity,omitempty"`
	Username          string `json:"username,omitempty"`
	PasswordSecretRef string `json:"passwordSecretRef,omitempty"`
}

// Secret is an app secret. Azure never returns its value.
type Secret struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// Template is an app's revision template.
type Template struct {
	Containers []Container `json:"containers"`
	Scale      *Scale      `json:"scale,omitempty"`
}

// Container is an app's container.
type Container struct {
	Name      string     `json:"name"`
	Image     string     `json:"image"`
	Env       []EnvVar   `json:"env,omitempty"`
	Resources *Resources `json:"resources,omitempty"`
}

// EnvVar is one variable: a value, or a reference to an app secret.
type EnvVar struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secretRef,omitempty"`
}

// Resources are a container's CPU and memory, such as 0.5 and "1Gi".
type Resources struct {
	CPU    float64 `json:"cpu"`
	Memory string  `json:"memory"`
}

// Scale bounds an app's replicas.
type Scale struct {
	MinReplicas int `json:"minReplicas"`
	MaxReplicas int `json:"maxReplicas,omitempty"`
}

// URL is where app answers, or "" when it takes no traffic. A test's
// fake serves its apps over plain HTTP.
func (s *Session) URL(app *App) string {
	if app.Properties.Configuration.Ingress == nil || app.Properties.Configuration.Ingress.FQDN == "" {
		return ""
	}
	scheme := "https://"
	if strings.HasPrefix(s.c.management, "http://") {
		scheme = "http://"
	}
	return scheme + app.Properties.Configuration.Ingress.FQDN
}

// Image is what the app's first container runs.
func (a *App) Image() string {
	if len(a.Properties.Template.Containers) == 0 {
		return ""
	}
	return a.Properties.Template.Containers[0].Image
}

// AppPath is the resource ID of the container app called name in the
// session's resource group.
func (s *Session) AppPath(name string) string {
	return s.group() + "/providers/Microsoft.App/containerApps/" + url.PathEscape(name)
}

// Owns reports whether id is a container app in the session's
// subscription, so nothing else is read or deleted through it.
func (s *Session) Owns(id string) bool {
	prefix := strings.ToLower(s.subscription() + "/resourceGroups/")
	lower := strings.ToLower(id)
	return strings.HasPrefix(lower, prefix) && strings.Contains(lower, "/providers/microsoft.app/containerapps/") && !strings.Contains(id, "..")
}

// GetApp reads a container app by resource ID; a missing one is
// IsNotFound.
func (s *Session) GetApp(ctx context.Context, id string) (*App, error) {
	if !s.Owns(id) {
		return nil, fmt.Errorf("%s isn't a container app in subscription %s", id, s.cred.SubscriptionID)
	}
	var a App
	if _, err := s.do(ctx, http.MethodGet, id, appsVersion, nil, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// PutApp creates or replaces the container app called name, and returns
// its resource ID without waiting for it.
func (s *Session) PutApp(ctx context.Context, name string, app App) (string, error) {
	var out App
	if _, err := s.do(ctx, http.MethodPut, s.AppPath(name), appsVersion, app, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		out.ID = s.AppPath(name)
	}
	return out.ID, nil
}

// revision is the part of a container app revision that says why it
// isn't serving.
type revision struct {
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
		ProvisioningError string `json:"provisioningError"`
		RunningState      string `json:"runningState"`
		RunningDetails    string `json:"runningStateDetails"`
	} `json:"properties"`
}

// PullError is a revision that didn't start because it couldn't pull
// its image, as while a pull identity's role propagates.
type PullError struct {
	Revision, Detail string
}

func (e *PullError) Error() string {
	return fmt.Sprintf("revision %s didn't start: %s", e.Revision, e.Detail)
}

// WaitApp waits until the app's newest revision is provisioned and ready,
// and fails if it can't be: with a PullError when the revision couldn't
// pull its image.
func (s *Session) WaitApp(ctx context.Context, id string) (*App, error) {
	for {
		a, err := s.GetApp(ctx, id)
		if err != nil {
			return nil, err
		}
		p := a.Properties
		switch strings.ToLower(p.ProvisioningState) {
		case "failed", "canceled":
			return nil, fmt.Errorf("azure couldn't deploy the container app: provisioning %s", strings.ToLower(p.ProvisioningState))
		case "succeeded":
			if p.LatestRevisionName != "" && p.LatestRevisionName == p.LatestReadyRevision {
				return a, nil
			}
			if p.LatestRevisionName != "" {
				var rev revision
				if _, err := s.do(ctx, http.MethodGet, id+"/revisions/"+url.PathEscape(p.LatestRevisionName), appsVersion, nil, &rev); err != nil && !IsNotFound(err) {
					return nil, err
				}
				rp := rev.Properties
				if strings.EqualFold(rp.ProvisioningState, "Failed") || strings.EqualFold(rp.RunningState, "Failed") {
					detail := strings.TrimSpace(rp.ProvisioningError + " " + rp.RunningDetails)
					if detail == "" {
						detail = "it failed"
					}
					if lower := strings.ToLower(detail); strings.Contains(lower, "pull") || strings.Contains(lower, "unauthorized") {
						return nil, &PullError{Revision: p.LatestRevisionName, Detail: detail}
					}
					return nil, fmt.Errorf("revision %s didn't start: %s", p.LatestRevisionName, detail)
				}
			}
		}
		progress(ctx, "Rolling out the new revision")
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
}

// DeleteApp deletes a container app and waits until it's gone. One
// that's already gone is not an error.
func (s *Session) DeleteApp(ctx context.Context, id string) error {
	if !s.Owns(id) {
		return fmt.Errorf("%s isn't a container app in subscription %s", id, s.cred.SubscriptionID)
	}
	_, err := s.do(ctx, http.MethodDelete, id, appsVersion, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		_, err := s.GetApp(ctx, id)
		if IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		progress(ctx, "Deleting the container app")
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}
