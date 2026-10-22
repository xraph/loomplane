package owned

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/ctrlplane/instance"
	cpprovider "github.com/xraph/ctrlplane/provider"
)

const (
	LabelOrg                  = "loomplane.org"
	LabelPlan                 = "loomplane.plan"
	LabelProject              = "loomplane.project"
	LabelEnvironment          = "loomplane.environment"
	LabelAction               = "loomplane.action"
	LabelService              = "loomplane.service"
	LabelOperation            = "loomplane.operation"
	LabelConnection           = "loomplane.connection"
	LabelSettings             = "loomplane.target.settings"
	LabelRuntime              = "loomplane.runtime.ref"
	LabelImage                = "loomplane.image.ref"
	LabelPublic               = "loomplane.public"
	LabelHostPort             = "loomplane.host.port"
	LabelHealth               = "loomplane.health.path"
	LabelHostname             = "loomplane.hostname"
	LabelIngressClass         = "loomplane.ingress.class"
	LabelTLSSecret            = "loomplane.tls.secret" //nolint:gosec // names a selected Secret, never its value
	LabelReplicas             = "loomplane.replicas"
	LabelScaleMin             = "loomplane.scale.min"
	LabelScaleMax             = "loomplane.scale.max"
	LabelArchitecture         = "loomplane.architecture"
	LabelRevision             = "loomplane.revision"
	LabelReceiptRelease       = "loomplane.runtime.receipt.release"
	LabelReceiptServicePrefix = "loomplane.runtime.receipt.service."
	LabelInstance             = "loomplane.instance"
	LabelManageIngress        = "loomplane.manage.ingress"
	LabelManageHPA            = "loomplane.manage.hpa"
)

// A composed catalog resource's component carries its kind, its resource's
// manifest name and its own name. LabelHealthPort is the container port
// its readiness probe answers on, when it publishes more than one.
const (
	LabelCatalogKind      = "loomplane.catalog.kind"
	LabelCatalogResource  = "loomplane.catalog.resource"
	LabelCatalogComponent = "loomplane.catalog.component"
	LabelHealthPort       = "loomplane.health.port"
)

type Instances interface {
	Get(context.Context, id.ID) (*instance.Instance, error)
}

type instanceUpdater interface {
	Update(context.Context, id.ID, instance.UpdateRequest) (*instance.Instance, error)
}

type RuntimeValues struct {
	Env      map[string]string
	PullAuth string
}
type Values func(context.Context, map[string]string) (RuntimeValues, error)

// Backend uses an explicitly selected connection for every operation. Its
// runtime values are resolved in memory; Ctrl Plane stores only references.
type Backend interface {
	Apply(context.Context, id.ID, cpprovider.ServiceSpec, map[string]string) (*cpprovider.ProvisionResult, error)
	Remove(context.Context, id.ID, map[string]string) error
	Status(context.Context, id.ID, map[string]string) (*cpprovider.InstanceStatus, error)
	SetRunning(context.Context, id.ID, map[string]string, bool) error
	Scale(context.Context, id.ID, map[string]string, cpprovider.ResourceSpec) error
}

// JobBackend runs a one-shot container to completion: a composed
// resource's migration, for one deployment's action. name identifies the
// run on the target, so a retry finds what an earlier attempt left. A
// finished run is a JobResult whatever its exit code; an error is a run
// that couldn't start or didn't finish within timeout. What it created is
// removed either way.
type JobBackend interface {
	RunJob(ctx context.Context, name string, spec cpprovider.ServiceSpec, labels map[string]string, timeout time.Duration) (JobResult, error)
}

// JobResult is how a job ended: its exit code and the last JobOutputLimit
// bytes of what it wrote, with every secret value replaced by "[redacted]".
type JobResult struct {
	ExitCode int
	Output   string
}

// JobOutputLimit is how much of a job's output its result keeps.
const JobOutputLimit = 16 << 10

// JobOutput is the end of what a job wrote, at most JobOutputLimit bytes,
// with each secret value replaced by "[redacted]". A value of several
// lines also has each of its lines replaced wherever it appears. Invalid
// UTF-8 is dropped first, so it can't keep a secret from being found.
// output must not start or end inside a secret: JobTail.Output makes sure
// of that for a job's log.
func JobOutput(output []byte, secrets map[string]string) string {
	var pieces []string
	for _, value := range secrets {
		value = strings.ToValidUTF8(value, "")
		if value == "" {
			continue
		}
		pieces = append(pieces, value)
		if strings.Contains(value, "\n") {
			for line := range strings.SplitSeq(value, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					pieces = append(pieces, line)
				}
			}
		}
	}
	// The longest first, so a secret containing another isn't left half
	// shown.
	slices.SortFunc(pieces, func(a, b string) int { return len(b) - len(a) })
	text := strings.ToValidUTF8(string(output), "")
	for _, piece := range pieces {
		text = strings.ReplaceAll(text, piece, "[redacted]")
	}
	// Nothing of a secret is left to cut through. A cut inside a
	// character drops what's left of it.
	if len(text) > JobOutputLimit {
		text = text[len(text)-JobOutputLimit:]
		for text != "" && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
	}
	return text
}

// jobLineTooLong stands in for a line longer than a JobTail keeps.
const jobLineTooLong = "[a line too long to show]"

// JobTail keeps the end of a job's log as it's read from a target, as
// whole lines, so that no secret in it is cut. Its invariant is that what
// it keeps starts at the start of a line and ends at the end of one:
//   - A target's log tail counts log entries, and a long line is several
//     entries, and rotation can drop the start of one, so each stream's
//     first line can start anywhere, inside a secret. It's dropped.
//   - Each stream's bytes are put together into lines before they're
//     kept, so stdout and stderr interleave line by line and a secret
//     written in pieces is still whole.
//   - A line longer than it keeps isn't kept, rather than kept from its
//     middle.
//   - When what it keeps outgrows it, the cut starts after the first line
//     break.
//
// A value without a line break can't then run past what's shown, and the
// lines of one with them are each redacted by JobOutput.
type JobTail struct {
	keep    int
	data    []byte
	cut     bool
	streams map[byte]*jobStream
}

// jobStream is one stream's line being put together.
type jobStream struct {
	started bool
	line    []byte
	long    bool
}

func NewJobTail(secrets map[string]string) *JobTail {
	keep := JobOutputLimit
	for _, value := range secrets {
		keep = max(keep, JobOutputLimit+len(value))
	}
	return &JobTail{keep: keep, streams: map[byte]*jobStream{}}
}

// Write keeps a log of one stream, or of streams the target merged.
func (t *JobTail) Write(p []byte) (int, error) {
	t.WriteStream(0, p)
	return len(p), nil
}

// WriteStream keeps bytes of one stream of a log: Docker's stdout is 1
// and its stderr 2.
func (t *JobTail) WriteStream(stream byte, p []byte) {
	s := t.streams[stream]
	if s == nil {
		s = &jobStream{}
		t.streams[stream] = s
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if s.started && !s.long {
				s.line = append(s.line, p...)
				if len(s.line) > t.keep {
					s.line, s.long = nil, true
				}
			}
			return
		}
		switch {
		case !s.started:
			s.started = true
		case s.long:
			t.keepLine([]byte(jobLineTooLong + "\n"))
		case len(s.line)+i >= t.keep:
			t.keepLine([]byte(jobLineTooLong + "\n"))
		default:
			t.keepLine(append(s.line, p[:i+1]...))
		}
		s.line, s.long = nil, false
		p = p[i+1:]
	}
}

func (t *JobTail) keepLine(line []byte) {
	t.data = append(t.data, line...)
	if len(t.data) > 2*t.keep {
		t.data = append([]byte(nil), t.data[len(t.data)-t.keep:]...)
		t.cut = true
	}
}

// Output is what's kept, as JobOutput gives it, followed by the end of
// each stream that has no line break of its own.
func (t *JobTail) Output(secrets map[string]string) string {
	data := slices.Clone(t.data)
	var ends [][]byte
	for _, stream := range slices.Sorted(maps.Keys(t.streams)) {
		switch s := t.streams[stream]; {
		case s.long:
			ends = append(ends, []byte(jobLineTooLong))
		case len(s.line) > 0:
			ends = append(ends, s.line)
		}
	}
	data = append(data, bytes.Join(ends, []byte("\n"))...)
	cut := t.cut
	if len(data) > t.keep {
		data, cut = data[len(data)-t.keep:], true
	}
	if cut {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	return JobOutput(data, secrets)
}

type Provider struct {
	Key       string
	Instances Instances
	Backend   Backend
	AutoScale bool
	Wait      time.Duration
}

var _ cpprovider.Provider = (*Provider)(nil)
var _ cpprovider.ReadinessWaiter = (*Provider)(nil)

func (p *Provider) Info() cpprovider.ProviderInfo {
	return cpprovider.ProviderInfo{Name: p.Key, Version: "v1"}
}
func (p *Provider) Capabilities() []cpprovider.Capability {
	caps := []cpprovider.Capability{cpprovider.CapProvision, cpprovider.CapDeploy}
	if p.AutoScale {
		caps = append(caps, cpprovider.CapAutoScale)
	}
	return caps
}
func (p *Provider) ReadinessPolicy() cpprovider.ReadinessPolicy {
	wait := p.Wait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	return cpprovider.ReadinessPolicy{Timeout: wait, Interval: time.Second}
}
func (p *Provider) Provision(ctx context.Context, r cpprovider.ProvisionRequest) (*cpprovider.ProvisionResult, error) {
	if len(r.Services) != 1 || r.Labels[LabelOrg] != r.TenantID {
		return nil, fmt.Errorf("owned target needs one service in its recorded organization")
	}
	return p.Backend.Apply(ctx, r.InstanceID, r.Services[0], maps.Clone(r.Labels))
}
func (p *Provider) Deploy(ctx context.Context, r cpprovider.DeployRequest) (*cpprovider.DeployResult, error) {
	updater, ok := p.Instances.(instanceUpdater)
	if !ok {
		return nil, fmt.Errorf("owned deployment requires persistent runtime receipts")
	}
	inst, err := p.Instances.Get(ctx, r.InstanceID)
	if err != nil {
		return nil, err
	}
	if len(inst.Services) != 1 {
		return nil, fmt.Errorf("owned target needs one service")
	}
	spec := inst.Services[0]
	for _, service := range r.Services {
		if service.Name == spec.Name {
			spec.Image = service.Image
			if service.Env != nil {
				spec.Env = service.Env
			}
		}
	}
	labels := maps.Clone(inst.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[LabelRevision] = r.ReleaseID.String()
	result, err := p.Backend.Apply(ctx, r.InstanceID, spec, labels)
	if err != nil {
		return nil, err
	}
	if result == nil || result.ServiceRefs[spec.Name] == "" {
		return nil, fmt.Errorf("owned deployment returned no exact service reference")
	}
	deadline := time.Now().Add(p.ReadinessPolicy().Timeout)
	for {
		status, statusErr := p.Backend.Status(ctx, r.InstanceID, labels)
		if statusErr != nil {
			return nil, statusErr
		}
		if status.Ready {
			// Controller Deploy does not refresh ServiceRefs. Record the ready
			// runtime separately; consumers also require its deployment to succeed.
			for key := range labels {
				if strings.HasPrefix(key, LabelReceiptServicePrefix) {
					delete(labels, key)
				}
			}
			labels[LabelReceiptRelease] = r.ReleaseID.String()
			for name, ref := range result.ServiceRefs {
				labels[LabelReceiptServicePrefix+name] = ref
			}
			if _, err = updater.Update(ctx, r.InstanceID, instance.UpdateRequest{Labels: labels}); err != nil {
				return nil, fmt.Errorf("persist owned runtime receipt: %w", err)
			}
			return &cpprovider.DeployResult{ProviderRef: result.ProviderRef, Status: "succeeded", Endpoints: result.Endpoints}, nil
		}
		if status.State == cpprovider.StateFailed || time.Now().After(deadline) {
			return nil, fmt.Errorf("owned target did not become ready: %s", status.Message)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (p *Provider) Deprovision(ctx context.Context, iid id.ID) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.Remove(ctx, iid, inst.Labels)
}
func (p *Provider) Status(ctx context.Context, iid id.ID) (*cpprovider.InstanceStatus, error) {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return nil, err
	}
	return p.Backend.Status(ctx, iid, inst.Labels)
}
func (p *Provider) Start(ctx context.Context, iid id.ID) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.SetRunning(ctx, iid, inst.Labels, true)
}
func (p *Provider) Stop(ctx context.Context, iid id.ID) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.SetRunning(ctx, iid, inst.Labels, false)
}
func (p *Provider) Restart(ctx context.Context, iid id.ID) error {
	if err := p.Stop(ctx, iid); err != nil {
		return err
	}
	return p.Start(ctx, iid)
}
func (p *Provider) Scale(ctx context.Context, iid id.ID, spec cpprovider.ResourceSpec) error {
	inst, err := p.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	return p.Backend.Scale(ctx, iid, inst.Labels, spec)
}
func (*Provider) Rollback(context.Context, id.ID, id.ID) error { return ctrlplane.ErrProviderUnavail }
func (*Provider) Resources(context.Context, id.ID) (*cpprovider.ResourceUsage, error) {
	return nil, ctrlplane.ErrProviderUnavail
}
func (*Provider) Logs(context.Context, id.ID, cpprovider.LogOptions) (io.ReadCloser, error) {
	return nil, ctrlplane.ErrProviderUnavail
}
func (*Provider) Exec(context.Context, id.ID, cpprovider.ExecRequest) (*cpprovider.ExecResult, error) {
	return nil, ctrlplane.ErrProviderUnavail
}

func ForOperation(labels map[string]string, operation string) map[string]string {
	out := maps.Clone(labels)
	out[LabelOperation] = operation
	return out
}
