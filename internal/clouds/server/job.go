package server

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// labelJob names the job a container runs, so a retry finds what an
// earlier attempt left.
const labelJob = "loomplane.job"

// jobName is a job's name: "lp-job-" and twelve hex digits.
var jobName = regexp.MustCompile(`^lp-job-[0-9a-f]{12}$`)

var _ owned.JobBackend = (*Runtime)(nil)

// RunJob runs a one-shot container on the environment's network, the way
// Apply runs a service's, waits up to timeout for it to exit and reads the
// end of its output. The container never restarts, and it's removed
// however the run ends.
func (r *Runtime) RunJob(ctx context.Context, name string, spec cpprovider.ServiceSpec, labels map[string]string, timeout time.Duration) (result owned.JobResult, err error) {
	if !jobName.MatchString(name) || timeout <= 0 {
		return result, errors.New("a job needs a job name and a timeout")
	}
	if len(spec.Volumes) > 0 || len(spec.Ports) > 0 || labels[owned.LabelPublic] == "true" || labels[LabelRecovery] != "" {
		return result, errors.New("a job runs without volumes or ports")
	}
	component := cmp.Or(labels[owned.LabelCatalogComponent], name)
	c, err := r.Session(ctx, labels)
	if err != nil {
		return result, err
	}
	defer c.Close()
	// An earlier attempt that never finished can leave its container.
	if removeErr := r.removeLeftoverJob(ctx, c, name, labels); removeErr != nil {
		return result, removeErr
	}
	network, err := r.network(ctx, c, labels)
	if err != nil {
		return result, err
	}
	values := owned.RuntimeValues{}
	if r.Values != nil {
		if values, err = r.Values(ctx, labels); err != nil {
			return result, err
		}
	}
	if !strings.HasPrefix(spec.Image, "sha256:") {
		if pullErr := r.pull(ctx, c, spec.Image, values.PullAuth); pullErr != nil {
			return result, pullErr
		}
	}
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, values.Env)
	variables := []string{}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		variables = append(variables, key+"="+env[key])
	}
	containerLabels := map[string]string{owned.LabelOrg: labels[owned.LabelOrg], owned.LabelProject: labels[owned.LabelProject], owned.LabelEnvironment: labels[owned.LabelEnvironment], owned.LabelConnection: labels[owned.LabelConnection], owned.LabelRevision: labels[owned.LabelRevision], labelJob: name}
	body := map[string]any{"Image": spec.Image, "Env": variables, "Labels": containerLabels, "HostConfig": map[string]any{"Init": true, "RestartPolicy": map[string]string{"Name": "no"}, "Memory": int64(spec.Resources.MemoryMB) * (1 << 20), "NanoCpus": int64(spec.Resources.CPUMillis) * 1000000}, "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{"Aliases": []string{name}}}}}
	if len(spec.Command) > 0 {
		body["Entrypoint"] = spec.Command
	}
	if len(spec.Args) > 0 {
		body["Cmd"] = spec.Args
	}
	var created struct {
		ID string `json:"Id"`
	}
	if createErr := dockerJSON(ctx, c, http.MethodPost, "/containers/create?name="+name, body, &created); createErr != nil {
		return result, createErr
	}
	container := "/containers/" + url.PathEscape(created.ID)
	timedOut := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if timedOut {
			// Removing it kills it too; killing it first stops it however
			// the removal goes.
			_ = dockerJSON(cleanup, c, http.MethodPost, container+"/kill", nil, nil) //nolint:errcheck // the forced removal below is what counts
		}
		if removeErr := dockerJSON(cleanup, c, http.MethodDelete, container+"?force=1", nil, nil); removeErr != nil && !errors.Is(removeErr, ErrMissing) {
			err = errors.Join(err, fmt.Errorf("remove the %s job's container: %w", component, removeErr))
		}
	}()
	if startErr := dockerJSON(ctx, c, http.MethodPost, container+"/start", nil, nil); startErr != nil {
		return result, startErr
	}
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var exited struct {
		StatusCode int
		Error      *struct{ Message string }
	}
	if waitErr := dockerJSON(wait, c, http.MethodPost, container+"/wait?condition=not-running", nil, &exited); waitErr != nil {
		if ctx.Err() == nil && errors.Is(wait.Err(), context.DeadlineExceeded) {
			timedOut = true
			// What it wrote so far says where it got to.
			read, cancelRead := context.WithTimeout(ctx, 30*time.Second)
			defer cancelRead()
			return result, jobLate(component, timeout, r.jobOutput(read, c, container, values.Env))
		}
		return result, waitErr
	}
	if exited.Error != nil && exited.Error.Message != "" {
		return result, fmt.Errorf("wait for the %s job: %s", component, owned.JobOutput([]byte(exited.Error.Message), values.Env))
	}
	return owned.JobResult{ExitCode: exited.StatusCode, Output: r.jobOutput(ctx, c, container, values.Env)}, nil
}

// jobLate is the error of a job that ran out of time, with what it wrote.
func jobLate(component string, timeout time.Duration, output string) error {
	if output == "" {
		return fmt.Errorf("%s did not finish within %s", component, timeout)
	}
	return fmt.Errorf("%s did not finish within %s: %s", component, timeout, output)
}

// jobOutput is the end of a job container's output, redacted. One that
// can't be read says so: the job ran, so it isn't run again over its
// output.
func (r *Runtime) jobOutput(ctx context.Context, c *Client, container string, secrets map[string]string) string {
	tail := owned.NewJobTail(secrets)
	if err := r.jobLogs(ctx, c, container, tail); err != nil {
		return owned.JobOutput([]byte("(its output couldn't be read: "+err.Error()+")"), secrets)
	}
	return tail.Output(secrets)
}

// removeLeftoverJob removes a container this job's earlier attempt left,
// and refuses one that's someone else's.
func (r *Runtime) removeLeftoverJob(ctx context.Context, c *Client, name string, labels map[string]string) error {
	var info containerInfo
	err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &info)
	if errors.Is(err, ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, key := range []string{owned.LabelOrg, owned.LabelProject, owned.LabelEnvironment} {
		if info.Config.Labels[key] == "" || info.Config.Labels[key] != labels[key] {
			return ErrOwned
		}
	}
	if info.Config.Labels[labelJob] != name {
		return ErrOwned
	}
	return dockerJSON(ctx, c, http.MethodDelete, "/containers/"+url.PathEscape(info.ID)+"?force=1", nil, nil)
}

// jobLogs reads the end of a container's output, stdout and stderr
// together, into tail.
func (r *Runtime) jobLogs(ctx context.Context, c *Client, container string, tail *owned.JobTail) error {
	res, err := c.Request(ctx, http.MethodGet, container+"/logs?stdout=1&stderr=1&tail=2000", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("docker returned HTTP %d", res.StatusCode)
	}
	// A container without a terminal multiplexes its streams: each frame
	// has an eight-byte header whose first byte names the stream and whose
	// last four give the frame's length.
	reader := bufio.NewReader(res.Body)
	for {
		header, peekErr := reader.Peek(8)
		if errors.Is(peekErr, io.EOF) && len(header) == 0 {
			return nil
		}
		if peekErr != nil || header[0] > 2 || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			// Not a frame: a terminal's raw output.
			if _, copyErr := io.Copy(tail, reader); copyErr != nil {
				return copyErr
			}
			return nil
		}
		if _, discardErr := reader.Discard(8); discardErr != nil {
			return discardErr
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, copyErr := io.CopyN(tail, reader, size); copyErr != nil {
			if errors.Is(copyErr, io.EOF) {
				return nil
			}
			return copyErr
		}
	}
}
