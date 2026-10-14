package deployments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/registry"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

const imageOwnerLabel = "loomplane.owner"
const imageSourceLabel = "loomplane.source"

var imageDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ImageArtifact is persisted in the executor checkpoint. Credentials remain
// in Vault; this records the reviewed source, placement and completed image.
type ImageArtifact struct {
	Reference    string `json:"reference"`
	Digest       string `json:"digest"`
	SourceID     string `json:"source_id"`
	Builder      string `json:"builder"`
	Registry     string `json:"registry"`
	Attempt      string `json:"attempt"`
	Operation    string `json:"operation"`
	Tag          string `json:"tag"`
	Architecture string `json:"architecture,omitempty"`
}

type ImageBuilder struct {
	Dial func(context.Context, *server.Config) (*server.Client, error)
}

func (b *ImageBuilder) connection(ctx context.Context, job *Job, id, cloud string, settings map[string]string, pull bool) (*connections.Connection, []byte, error) {
	if job.BuildCredentials == nil || job.Deployment == nil {
		return nil, nil, errors.New("the deployment has no build credential source")
	}
	c, raw, err := job.BuildCredentials.Credential(ctx, job.Deployment.OrgID, id)
	if err != nil {
		return nil, nil, err
	}
	if c == nil || c.ID != id || c.OrgID != job.Deployment.OrgID || c.Cloud != cloud || (c.Status != connections.StatusReady && (!pull || !connections.RegistryPullReady(c))) {
		return nil, nil, errors.New("the reviewed build connection is not ready in this organization")
	}
	if !reflect.DeepEqual(settings, planning.ReviewedConnectionSettings(c.Cloud, c.Details)) {
		return nil, nil, errors.New("build connection settings changed since review; plan again")
	}
	return c, raw, nil
}

// Build runs only the source job.Source opens from the approved lockset. Its
// checkpoint belongs to the caller, so rollout state can share that checkpoint.
func (b *ImageBuilder) Build(ctx context.Context, job *Job, state *ImageArtifact, save func() error) (reference string, err error) {
	if job.Action.Service == nil || job.Action.Service.BuildTarget == nil || state == nil || save == nil {
		return "", errors.New("the service needs reviewed remote build settings")
	}
	target := job.Action.Service.BuildTarget
	collector := job.Collector("image-build", job.LogAttempt())
	defer func() { err = collector.safeError(err) }()
	var registryConfig *registry.Config
	if target.Registry != "" {
		connection, raw, credentialErr := b.connection(ctx, job, target.Registry, connections.Registry, target.RegistrySettings, target.Image != "")
		if credentialErr != nil {
			return "", credentialErr
		}
		collector.addCredential(raw)
		if target.Image == "" && connection.Details["push"] != "ready" {
			return "", errors.New("a source build needs registry push access")
		}
		registryConfig, err = registry.ParseConfig(raw, connection.Details)
		if err != nil {
			return "", err
		}
		if !reflect.DeepEqual(target.RegistrySettings, planning.ReviewedConnectionSettings(connections.Registry, registryConfig.Details)) {
			return "", errors.New("registry trust settings changed since review; plan again")
		}
	}
	if target.Image != "" {
		state.Reference = target.Image
		_, state.Digest, _ = strings.Cut(target.Image, "@")
		if !imageDigest.MatchString(state.Digest) {
			return "", errors.New("choose a digest-pinned existing image")
		}
		if registryConfig != nil {
			endpoint, parseErr := url.Parse(registryConfig.Endpoint)
			if parseErr != nil {
				return "", parseErr
			}
			if !strings.HasPrefix(target.Image, endpoint.Host+"/"+registryConfig.Repository+"/") {
				return "", errors.New("the pinned image is outside the selected registry prefix")
			}
		}
		state.Registry = target.Registry
		if saveErr := save(); saveErr != nil {
			return "", saveErr
		}
		return state.Reference, nil
	}
	if job.SourceID == "" || job.Source == nil {
		return "", errors.New("the approved source artifact is missing")
	}
	builder, raw, err := b.connection(ctx, job, target.Builder, connections.Server, target.BuilderSettings, false)
	if err != nil {
		return "", err
	}
	collector.addCredential(raw)
	config, err := server.ParseConfig(raw, builder.Details)
	if err != nil {
		return "", err
	}
	dial := b.Dial
	if dial == nil {
		dial = server.Dial
	}
	client, err := dial(ctx, config)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	details, missing, err := client.Check(ctx)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 || !reflect.DeepEqual(target.BuilderSettings, planning.ReviewedConnectionSettings(connections.Server, details)) {
		return "", errors.New("the builder identity or Docker capabilities changed; check the connection and plan again")
	}
	if state.Reference != "" {
		if state.SourceID != job.SourceID || state.Builder != target.Builder || state.Registry != target.Registry {
			return "", errors.New("the saved image belongs to another source or build target")
		}
		if registryConfig == nil {
			if _, err = imageInspect(ctx, client, state.Reference, job); err != nil {
				return "", err
			}
		}
		previous := job.Collector(state.Operation, state.Attempt)
		if checkpoint, readErr := previous.Saved(ctx); readErr == nil && checkpoint.State == "collecting" {
			previous.RecordState(ctx, "", "incomplete", "The image checkpoint was retained before the build output collector finished.")
		}
		return state.Reference, nil
	}
	if state.Operation != "" {
		previous := job.Collector(state.Operation, state.Attempt)
		previous.RecordState(ctx, "", "incomplete", "The builder connection ended before the image checkpoint. This attempt will be rebuilt.")
	}
	operation, err := typeid.Generate("build")
	if err != nil {
		return "", err
	}
	state.SourceID, state.Builder, state.Registry, state.Attempt, state.Operation = job.SourceID, target.Builder, target.Registry, job.LogAttempt(), operation.String()
	state.Architecture = details["architecture"]
	collector = job.Collector(state.Operation, state.Attempt)
	defer func() {
		if err != nil {
			collector.RecordState(context.WithoutCancel(ctx), "", "incomplete", err.Error())
		}
	}()
	collector.addCredential(raw)
	if registryConfig != nil {
		collector.addCredential([]byte(registryConfig.Password))
		collector.addCredential([]byte(registryConfig.Token))
	}
	owner := imageOwner(job)
	tagHash := sha256.Sum256([]byte(job.SourceID + "/" + state.Operation + "/" + owner))
	state.Tag = "loomplane/" + imageName(job) + ":" + fmt.Sprintf("%x", tagHash[:12])
	if registryConfig != nil {
		endpoint, parseErr := url.Parse(registryConfig.Endpoint)
		if parseErr != nil {
			return "", parseErr
		}
		state.Tag = endpoint.Host + "/" + registryConfig.Repository + "/" + state.Tag
	}
	source, _, err := job.Source(ctx)
	if err != nil {
		return "", err
	}
	prepared, err := prepareImageContext(source, job.Action.Service)
	closeErr := source.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if saveErr := save(); saveErr != nil {
		return "", saveErr
	}
	collector.RecordState(ctx, "", "collecting", "Building on "+builder.Name)
	labels, marshalErr := json.Marshal(map[string]string{imageOwnerLabel: owner, imageSourceLabel: job.SourceID, "loomplane.operation": state.Operation})
	if marshalErr != nil {
		return "", marshalErr
	}
	query := url.Values{"dockerfile": {prepared.Dockerfile}, "t": {state.Tag}, "version": {"2"}, "rm": {"1"}, "forcerm": {"1"}, "labels": {string(labels)}}
	job.Progress(StepBuilding, "Building on "+builder.Name)
	response, err := dockerRequest(ctx, client, http.MethodPost, "/build?"+query.Encode(), bytes.NewReader(prepared.Tar), "application/x-tar", nil)
	if err != nil {
		return "", err
	}
	_, err = readBuildOutput(response.Body, func(line buildLine) error { return collector.Emit(ctx, line) })
	_ = response.Body.Close()
	if err != nil {
		return "", err
	}
	image, err := imageInspect(ctx, client, state.Tag, job)
	if err != nil {
		return "", err
	}
	digest, reference := image.ID, image.ID
	if registryConfig != nil {
		job.Progress(StepBuilding, "Publishing the built image")
		repository, tag, _ := strings.Cut(state.Tag, ":")
		// A registry host can itself contain a port. The tag follows the last colon.
		if colon := strings.LastIndex(state.Tag, ":"); colon > strings.LastIndex(state.Tag, "/") {
			repository, tag = state.Tag[:colon], state.Tag[colon+1:]
		}
		endpoint, parseErr := url.Parse(registryConfig.Endpoint)
		if parseErr != nil {
			return "", parseErr
		}
		auth, authErr := registry.New(registryConfig).DockerAuth(ctx, strings.TrimPrefix(repository, endpoint.Host+"/"), "pull,push")
		if authErr != nil {
			return "", authErr
		}
		collector.addCredential(auth)
		response, err = dockerRequest(ctx, client, http.MethodPost, "/images/"+url.PathEscape(repository)+"/push?tag="+url.QueryEscape(tag), nil, "", http.Header{"X-Registry-Auth": {base64.URLEncoding.EncodeToString(auth)}})
		if err != nil {
			return "", err
		}
		published, pushErr := readBuildOutput(response.Body, func(line buildLine) error {
			line.ID = "push/" + line.ID
			line.Source = "registry"
			return collector.Emit(ctx, line)
		})
		_ = response.Body.Close()
		if pushErr != nil {
			return "", pushErr
		}
		if !imageDigest.MatchString(published) {
			return "", errors.New("the registry did not return a published image digest")
		}
		digest, reference = published, repository+"@"+published
	}
	if !imageDigest.MatchString(digest) {
		return "", errors.New("the builder did not return a valid image digest")
	}
	state.Digest, state.Reference = digest, reference
	if saveErr := save(); saveErr != nil {
		return "", saveErr
	}
	collector.RecordState(ctx, "", "complete", "Image ready: "+state.Reference)
	return state.Reference, nil
}

func imageOwner(job *Job) string {
	return job.Deployment.OrgID + "/" + job.Deployment.EnvironmentID + "/" + job.Action.ID
}

type builtImage struct {
	ID     string                             `json:"Id"`
	Config struct{ Labels map[string]string } `json:"Config"`
}

func imageInspect(ctx context.Context, client *server.Client, reference string, job *Job) (*builtImage, error) {
	response, err := dockerRequest(ctx, client, http.MethodGet, "/images/"+url.PathEscape(reference)+"/json", nil, "", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var image builtImage
	if decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&image); decodeErr != nil {
		return nil, decodeErr
	}
	if image.Config.Labels[imageOwnerLabel] != imageOwner(job) || image.Config.Labels[imageSourceLabel] != job.SourceID || !imageDigest.MatchString(image.ID) {
		return nil, errors.New("the built image does not belong to the reviewed source and service")
	}
	return &image, nil
}

func dockerRequest(ctx context.Context, client *server.Client, method, resource string, body io.Reader, contentType string, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+resource, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := client.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	var message struct{ Message string }
	if decodeErr := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&message); decodeErr != nil {
		message.Message = http.StatusText(response.StatusCode)
	}
	return nil, fmt.Errorf("docker returned HTTP %d: %s", response.StatusCode, message.Message)
}
