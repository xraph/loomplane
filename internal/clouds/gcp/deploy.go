package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	cloudprogress "github.com/xraph/loomplane/internal/clouds/progress"
)

func progress(ctx context.Context, message string) { cloudprogress.Report(ctx, message) }

func (s *Session) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.c.poll):
		return nil
	}
}

// --- Cloud Storage ---

// EnsureBucket creates a bucket in the project, if it isn't there.
func (s *Session) EnsureBucket(ctx context.Context, name, location string) error {
	u := s.c.url("storage.googleapis.com", "/storage/v1/b?project="+url.QueryEscape(s.key.ProjectID))
	err := s.do(ctx, http.MethodPost, u, map[string]any{
		"name": name, "location": location, "iamConfiguration": map[string]any{"uniformBucketLevelAccess": map[string]bool{"enabled": true}},
	}, nil)
	if IsConflict(err) {
		return nil
	}
	return err
}

// Upload writes an object.
func (s *Session) Upload(ctx context.Context, bucket, object string, r io.Reader, size int64, contentType string) error {
	u := s.c.url("storage.googleapis.com", "/upload/storage/v1/b/"+url.PathEscape(bucket)+"/o?uploadType=media&name="+url.QueryEscape(object))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	return s.send(req, nil)
}

// --- Artifact Registry ---

// EnsureRepository creates a Docker repository in region, if it isn't
// there, and waits until it can be pushed to.
func (s *Session) EnsureRepository(ctx context.Context, region, repo string) error {
	base := "/v1/projects/" + s.key.ProjectID + "/locations/" + region + "/repositories"
	err := s.do(ctx, http.MethodPost, s.c.url("artifactregistry.googleapis.com", base+"?repositoryId="+url.QueryEscape(repo)),
		map[string]any{"format": "DOCKER", "description": "Images Loomplane built"}, nil)
	if err != nil && !IsConflict(err) {
		return err
	}
	for {
		err := s.do(ctx, http.MethodGet, s.c.url("artifactregistry.googleapis.com", base+"/"+repo), nil, nil)
		if err == nil {
			return nil
		}
		if !IsNotFound(err) {
			return err
		}
		progress(ctx, "Creating the image repository")
		if err := s.wait(ctx); err != nil {
			return err
		}
	}
}

// ImageDigest reads the digest a Docker repository's tag points at, for
// an image the build pushed itself rather than through Cloud Build's
// images, as pack does. It's empty when the tag names no version.
func (s *Session) ImageDigest(ctx context.Context, region, repo, image, tag string) (string, error) {
	var t struct {
		Version string `json:"version"`
	}
	p := "/v1/projects/" + s.key.ProjectID + "/locations/" + region + "/repositories/" + repo +
		"/packages/" + url.PathEscape(image) + "/tags/" + url.PathEscape(tag)
	if err := s.do(ctx, http.MethodGet, s.c.url("artifactregistry.googleapis.com", p), nil, &t); err != nil {
		return "", err
	}
	_, digest, _ := strings.Cut(t.Version, "/versions/")
	return digest, nil
}

// ImageName is where Loomplane pushes a service's image.
func ImageName(project, region, repo, name, tag string) string {
	return region + "-docker.pkg.dev/" + project + "/" + repo + "/" + name + ":" + tag
}

// --- Cloud Build ---

// BuildStep is one step of a Cloud Build.
type BuildStep struct {
	Name       string   `json:"name"`
	Entrypoint string   `json:"entrypoint,omitempty"`
	Args       []string `json:"args,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	Env        []string `json:"env,omitempty"`
}

// Build is a Cloud Build request.
type Build struct {
	Source  BuildSource       `json:"source"`
	Steps   []BuildStep       `json:"steps"`
	Images  []string          `json:"images,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
	Tags    []string          `json:"tags,omitempty"`
	Options map[string]string `json:"options,omitempty"`
}

// BuildSource is where a build's files come from.
type BuildSource struct {
	StorageSource struct {
		Bucket string `json:"bucket"`
		Object string `json:"object"`
	} `json:"storageSource"`
}

// BuildStatus is how a build stands.
type BuildStatus struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	StatusDetail string `json:"statusDetail"`
	LogURL       string `json:"logUrl"`
	Results      struct {
		Images []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"images"`
	} `json:"results"`
}

// Done reports whether the build has finished, either way.
func (b *BuildStatus) Done() bool {
	switch b.Status {
	case "SUCCESS", "FAILURE", "INTERNAL_ERROR", "TIMEOUT", "CANCELLED", "EXPIRED":
		return true
	}
	return false
}

// StartBuild starts a build and returns its ID.
func (s *Session) StartBuild(ctx context.Context, b Build) (string, error) {
	var op struct {
		Metadata struct {
			Build struct {
				ID string `json:"id"`
			} `json:"build"`
		} `json:"metadata"`
	}
	if err := s.do(ctx, http.MethodPost, s.c.url("cloudbuild.googleapis.com", "/v1/projects/"+s.key.ProjectID+"/builds"), b, &op); err != nil {
		return "", err
	}
	if op.Metadata.Build.ID == "" {
		return "", errors.New("cloud build started a build without an ID")
	}
	return op.Metadata.Build.ID, nil
}

// GetBuild reads a build.
func (s *Session) GetBuild(ctx context.Context, id string) (*BuildStatus, error) {
	var b BuildStatus
	err := s.do(ctx, http.MethodGet, s.c.url("cloudbuild.googleapis.com", "/v1/projects/"+s.key.ProjectID+"/builds/"+url.PathEscape(id)), nil, &b)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// WaitBuild polls a build until it finishes, and fails unless it
// succeeded. It returns the digest of the image it pushed, if any.
func (s *Session) WaitBuild(ctx context.Context, id string) (*BuildStatus, error) {
	for {
		b, err := s.GetBuild(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Done() {
			if b.Status != "SUCCESS" {
				detail := strings.TrimSpace(b.StatusDetail)
				if detail == "" {
					detail = strings.ToLower(b.Status)
				}
				return b, fmt.Errorf("cloud build %s: %s. Its log: %s", id, detail, b.LogURL)
			}
			return b, nil
		}
		progress(ctx, "Building ("+strings.ToLower(b.Status)+")")
		if err := s.wait(ctx); err != nil {
			return nil, err
		}
	}
}

// --- Cloud Run ---

// RunService is the part of a Cloud Run v2 service Loomplane sets.
type RunService struct {
	Name     string            `json:"name,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Ingress  string            `json:"ingress,omitempty"`
	Template RunTemplate       `json:"template"`
	// Traffic splits requests between revisions. Left out, Cloud Run sends
	// all of them to the latest ready revision, which is what an ordinary
	// deploy wants. Only a canary sets it.
	Traffic []RunTraffic `json:"traffic,omitempty"`
	// Read only.
	URI                string         `json:"uri,omitempty"`
	LatestReadyRev     string         `json:"latestReadyRevision,omitempty"`
	LatestCreatedRev   string         `json:"latestCreatedRevision,omitempty"`
	TerminalCondition  *RunCondition  `json:"terminalCondition,omitempty"`
	Reconciling        bool           `json:"reconciling,omitempty"`
	ObservedGeneration string         `json:"observedGeneration,omitempty"`
	Conditions         []RunCondition `json:"conditions,omitempty"`
	// TrafficStatuses is how traffic is split now.
	TrafficStatuses []RunTraffic `json:"trafficStatuses,omitempty"`
}

// How a traffic target picks its revision.
const (
	// TrafficLatest follows the service's latest ready revision.
	TrafficLatest = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
	// TrafficRevision names its revision.
	TrafficRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
)

// RunTraffic is one share of a service's traffic: the latest ready
// revision's, or a named one's.
type RunTraffic struct {
	Type     string `json:"type"`
	Revision string `json:"revision,omitempty"`
	Percent  int    `json:"percent"`
}

// LatestReady is the short name of the service's latest ready revision.
func (s *RunService) LatestReady() string { return RevisionName(s.LatestReadyRev) }

// LatestCreated is the short name of the service's latest revision.
func (s *RunService) LatestCreated() string { return RevisionName(s.LatestCreatedRev) }

// RevisionName is a revision's name without the resource path Cloud Run
// may put in front of it.
func RevisionName(name string) string { return name[strings.LastIndex(name, "/")+1:] }

// RunTemplate is a service's revision template.
type RunTemplate struct {
	// Revision names the revision the template makes. Left empty, Cloud
	// Run names it. Only a canary's candidate is named, so the canary can
	// tell it from the baseline.
	Revision   string            `json:"revision,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Scaling    *RunScaling       `json:"scaling,omitempty"`
	Containers []RunContainer    `json:"containers"`
	Volumes    []RunVolume       `json:"volumes,omitempty"`
	VPCAccess  *RunVPCAccess     `json:"vpcAccess,omitempty"`
}

// RunVPCAccess sends a revision's traffic to private addresses through a
// VPC network: Direct VPC egress, which reaches Memorystore.
type RunVPCAccess struct {
	NetworkInterfaces []RunNetworkInterface `json:"networkInterfaces"`
	Egress            string                `json:"egress"`
}

// RunNetworkInterface is the network and subnet a revision's traffic
// leaves through.
type RunNetworkInterface struct {
	Network    string `json:"network"`
	Subnetwork string `json:"subnetwork"`
}

// RunVolume is a volume a revision mounts: here, the Cloud SQL
// connector's socket directory.
type RunVolume struct {
	Name             string       `json:"name"`
	EmptyDir         *RunEmptyDir `json:"emptyDir,omitempty"`
	CloudSQLInstance *struct {
		Instances []string `json:"instances"`
	} `json:"cloudSqlInstance,omitempty"`
}

// RunEmptyDir bounds a shared in-memory volume.
type RunEmptyDir struct {
	Medium    string `json:"medium"`
	SizeLimit string `json:"sizeLimit"`
}

// RunVolumeMount mounts a volume into a container.
type RunVolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// RunScaling bounds a service's instances.
type RunScaling struct {
	Min int `json:"minInstanceCount,omitempty"`
	Max int `json:"maxInstanceCount,omitempty"`
}

// RunContainer is a service's container.
type RunContainer struct {
	Name         string           `json:"name,omitempty"`
	Args         []string         `json:"args,omitempty"`
	DependsOn    []string         `json:"dependsOn,omitempty"`
	StartupProbe *RunProbe        `json:"startupProbe,omitempty"`
	Image        string           `json:"image"`
	Env          []RunEnv         `json:"env,omitempty"`
	Ports        []RunPort        `json:"ports,omitempty"`
	Resources    *RunResources    `json:"resources,omitempty"`
	Mounts       []RunVolumeMount `json:"volumeMounts,omitempty"`
}

// RunProbe checks a container before its dependants start.
type RunProbe struct {
	HTTPGet          RunHTTPGet `json:"httpGet"`
	TimeoutSeconds   int        `json:"timeoutSeconds"`
	PeriodSeconds    int        `json:"periodSeconds"`
	FailureThreshold int        `json:"failureThreshold"`
}

type RunHTTPGet struct {
	Path string `json:"path"`
	Port int    `json:"port"`
}

// RunEnv is one variable: a value, or a reference to a secret.
type RunEnv struct {
	Name        string          `json:"name"`
	Value       string          `json:"value,omitempty"`
	ValueSource *RunValueSource `json:"valueSource,omitempty"`
}

// RunValueSource reads a variable from Secret Manager.
type RunValueSource struct {
	SecretKeyRef struct {
		Secret  string `json:"secret"`
		Version string `json:"version"`
	} `json:"secretKeyRef"`
}

// RunPort is the port a container listens on.
type RunPort struct {
	ContainerPort int `json:"containerPort"`
}

// RunResources limits a container.
type RunResources struct {
	Limits  map[string]string `json:"limits,omitempty"`
	CPUIdle bool              `json:"cpuIdle,omitempty"`
}

// RunCondition is a service's readiness.
type RunCondition struct {
	Type    string `json:"type"`
	State   string `json:"state"`
	Message string `json:"message"`
}

func (s *Session) servicePath(region, name string) string {
	return "/v2/projects/" + s.key.ProjectID + "/locations/" + region + "/services/" + name
}

// GetService reads a service; a missing one is IsNotFound.
func (s *Session) GetService(ctx context.Context, region, name string) (*RunService, error) {
	var svc RunService
	if err := s.do(ctx, http.MethodGet, s.c.url("run.googleapis.com", s.servicePath(region, name)), nil, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

type operation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ApplyService creates the service, or replaces its template, and waits
// until the new revision serves.
func (s *Session) ApplyService(ctx context.Context, region, name string, svc RunService) (*RunService, error) {
	var op operation
	_, err := s.GetService(ctx, region, name)
	switch {
	case IsNotFound(err):
		u := s.c.url("run.googleapis.com", "/v2/projects/"+s.key.ProjectID+"/locations/"+region+"/services?serviceId="+url.QueryEscape(name))
		err = s.do(ctx, http.MethodPost, u, svc, &op)
	case err == nil:
		err = s.do(ctx, http.MethodPatch, s.c.url("run.googleapis.com", s.servicePath(region, name)), svc, &op)
	}
	if err != nil {
		return nil, err
	}
	if err := s.waitOperation(ctx, op, "Rolling out the new revision"); err != nil {
		return nil, err
	}
	return s.GetService(ctx, region, name)
}

func (s *Session) waitOperation(ctx context.Context, op operation, message string) error {
	for !op.Done {
		progress(ctx, message)
		if err := s.wait(ctx); err != nil {
			return err
		}
		if err := s.do(ctx, http.MethodGet, s.c.url("run.googleapis.com", "/v2/"+op.Name), nil, &op); err != nil {
			return err
		}
	}
	if op.Error != nil {
		return fmt.Errorf("cloud run: %s", op.Error.Message)
	}
	return nil
}

// Public is the member that lets anyone call a service.
const Public = "allUsers"

// SetInvokers sets who may call the service, replacing whoever could
// before: Public for anyone, or a service account. An organization
// policy that forbids public services makes Public fail, and the error
// says so.
func (s *Session) SetInvokers(ctx context.Context, region, name string, members ...string) error {
	u := s.c.url("run.googleapis.com", s.servicePath(region, name)+":setIamPolicy")
	err := s.do(ctx, http.MethodPost, u, map[string]any{
		"policy": map[string]any{"bindings": []map[string]any{{"role": "roles/run.invoker", "members": members}}},
	}, nil)
	if err != nil {
		return fmt.Errorf("set who may call %s: %w", name, err)
	}
	return nil
}

// DeleteService deletes a service and waits until it's gone. One that's
// already gone is not an error.
func (s *Session) DeleteService(ctx context.Context, region, name string) error {
	var op operation
	err := s.do(ctx, http.MethodDelete, s.c.url("run.googleapis.com", s.servicePath(region, name)), nil, &op)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.waitOperation(ctx, op, "Deleting the service")
}
