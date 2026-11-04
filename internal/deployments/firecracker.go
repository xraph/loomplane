package deployments

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/ctrlplane"
	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	fc "github.com/xraph/loomplane/internal/firecracker"
	"github.com/xraph/loomplane/internal/planning"
)

// firecrackerBackend runs one stateless application per instance on a
// Firecracker host. Where the host is, and the scope the instance runs under
// there, come from its session: an organization's connection opens that
// connection's host, and Loomplane Cloud opens the platform host the
// instance is placed on. Everything the backend does to a VM goes through
// the same code either way.
type firecrackerBackend struct {
	target *OwnedTarget
	// session opens the host the instance runs on and names its scope
	// there. The caller closes the client.
	session firecrackerSession
	// place opens the host to apply the instance on and returns what to do
	// once the host has applied it, which may give the instance a public
	// endpoint. Nil applies through session.
	place func(ctx context.Context, iid id.ID, labels map[string]string, spec fc.Spec) (*fc.Client, fc.Scope, firecrackerApplied, error)
	// effect stops or removes the instance, running act against each
	// host and scope it may run under. Nil acts once, through session.
	effect func(ctx context.Context, iid id.ID, labels map[string]string, action string, act func(*fc.Client, fc.Scope) error) error
	// redact turns a failure into what the workspace may read. Nil passes
	// it on.
	redact func(ctx context.Context, op string, err error) error
	// unpublished leaves the host's own address out of the instance's
	// endpoints and publishes no port on it.
	unpublished bool
}

type firecrackerSession func(ctx context.Context, iid id.ID, labels map[string]string) (*fc.Client, fc.Scope, error)

// firecrackerApplied runs once the host has applied an instance. A non-nil
// endpoint is where the instance is reached publicly.
type firecrackerApplied func(ctx context.Context, r fc.Record) (*cpprovider.Endpoint, error)

// orgFirecracker is the backend for an organization's Firecracker
// connection: every session opens that connection's host, after the
// connection checks it.
func (e *OwnedTarget) orgFirecracker(org, connection string) *firecrackerBackend {
	return &firecrackerBackend{target: e, session: func(ctx context.Context, iid id.ID, labels map[string]string) (*fc.Client, fc.Scope, error) {
		client, err := e.firecrackerSession(ctx, org, connection, labels)
		if err != nil {
			return nil, fc.Scope{}, err
		}
		return client, fcScope(iid, labels), nil
	}}
}

func (b *firecrackerBackend) fail(ctx context.Context, op string, err error) error {
	if err == nil || b.redact == nil {
		return err
	}
	return b.redact(ctx, op, err)
}

func (e *OwnedTarget) firecrackerSession(ctx context.Context, org, connection string, labels map[string]string) (*fc.Client, error) {
	c, raw, err := e.connection(ctx, org, connection, labels)
	if err != nil {
		return nil, err
	}
	client, err := fc.NewClient(raw, c.Details)
	if err != nil {
		return nil, err
	}
	_, err = client.Check(ctx)
	if err == nil && !reflect.DeepEqual(planning.ReviewedConnectionSettings(connections.Firecracker, client.Details), planning.ReviewedConnectionSettings(connections.Firecracker, c.Details)) {
		err = errors.New("firecracker runtime changed since review; check the connection and plan again")
	}
	if err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}
func fcScope(iid id.ID, labels map[string]string) fc.Scope {
	return fc.Scope{Org: labels[owned.LabelOrg], Connection: labels[owned.LabelConnection], Project: labels[owned.LabelProject], Environment: labels[owned.LabelEnvironment], Resource: iid.String()}
}
func firecrackerSpec(sp cpprovider.ServiceSpec, labels map[string]string) (fc.Spec, error) {
	if len(sp.Volumes) > 0 || len(sp.ConfigFiles) > 0 || sp.Resources.GPU != "" || labels[owned.LabelGPU] != "" || len(sp.Ports) > 1 || sp.Resources.Replicas > 1 || labels[owned.LabelCatalogKind] != "" {
		return fc.Spec{}, errors.New("firecracker supports one stateless application per instance without volumes, GPUs or catalog compositions")
	}
	q := fc.Spec{Kind: "app", Service: labels[owned.LabelService], Image: sp.Image, VCPUs: max(1, (sp.Resources.CPUMillis+999)/1000), MemoryMiB: max(128, sp.Resources.MemoryMB), DiskMiB: 4096, Command: sp.Command, Args: sp.Args, HealthPath: labels[owned.LabelHealth]}
	if len(sp.Ports) == 1 {
		// A service's port is "http" (spec) and a component's "tcp"; the
		// VM serves either over TCP.
		if p := sp.Ports[0].Protocol; p != "" && p != "tcp" && p != "http" {
			return q, fc.ErrInvalid
		}
		q.Port = sp.Ports[0].Container
	}
	if labels[owned.LabelPublic] == "true" {
		port, err := strconv.Atoi(labels[owned.LabelHostPort])
		if err != nil {
			return q, fc.ErrInvalid
		}
		q.PublishedPort = port
	}
	return q, nil
}
func firecrackerRegistry(raw []byte) ([]byte, error) {
	var auth struct{ Username, Password, Auth, IdentityToken, RegistryToken, ServerAddress string }
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, err
	}
	if auth.ServerAddress == "" {
		return nil, errors.New("registry credential has no server address")
	}
	if auth.Auth == "" && auth.Username != "" {
		auth.Auth = base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
	}
	return json.Marshal(map[string]any{"auths": map[string]any{auth.ServerAddress: map[string]string{"auth": auth.Auth, "identitytoken": auth.IdentityToken, "registrytoken": auth.RegistryToken}}})
}
func (b *firecrackerBackend) Apply(ctx context.Context, iid id.ID, sp cpprovider.ServiceSpec, labels map[string]string) (*cpprovider.ProvisionResult, error) {
	out, err := b.apply(ctx, iid, sp, labels)
	return out, b.fail(ctx, "apply", err)
}

func (b *firecrackerBackend) apply(ctx context.Context, iid id.ID, sp cpprovider.ServiceSpec, labels map[string]string) (*cpprovider.ProvisionResult, error) {
	specLabels := labels
	if b.unpublished {
		specLabels = maps.Clone(labels)
		specLabels[owned.LabelPublic] = "false"
	}
	spec, err := firecrackerSpec(sp, specLabels)
	if err != nil {
		return nil, err
	}
	place := b.place
	if place == nil {
		place = func(ctx context.Context, iid id.ID, labels map[string]string, _ fc.Spec) (*fc.Client, fc.Scope, firecrackerApplied, error) {
			client, scope, sessionErr := b.session(ctx, iid, labels)
			return client, scope, func(context.Context, fc.Record) (*cpprovider.Endpoint, error) { return nil, nil }, sessionErr
		}
	}
	client, scope, applied, err := place(ctx, iid, labels, spec)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	values, err := b.target.values(ctx, labels)
	if err != nil {
		return nil, err
	}
	if len(values.Files) > 0 {
		return nil, fc.ErrInvalid
	}
	env := maps.Clone(sp.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, values.Env)
	q := fc.Request{Scope: scope, RuntimeIdentity: client.Details["runtime_identity"], Spec: spec, Values: fc.Values{Env: env}}
	if values.PullAuth != "" {
		raw, e := base64.URLEncoding.DecodeString(values.PullAuth)
		if e != nil {
			return nil, e
		}
		q.Values.RegistryConfig, err = firecrackerRegistry(raw)
		if err != nil {
			return nil, err
		}
	}
	if b.target.FirecrackerAuthority == nil {
		return nil, fc.ErrUnavailable
	}
	q.Authority, err = b.target.FirecrackerAuthority.IssueFirecracker(ctx, q.Scope, "apply/"+labels[owned.LabelRevision], firecrackerDigest(struct {
		Spec         fc.Spec
		Runtime, Ref string
	}{spec, q.RuntimeIdentity, labels[owned.LabelRuntime]}))
	if err != nil {
		return nil, err
	}
	r, err := client.Apply(ctx, q)
	if err != nil {
		return nil, err
	}
	if r.Scope != q.Scope || r.Generation != q.Authority.Generation || r.Current == nil {
		return nil, fc.ErrConflict
	}
	public, appliedErr := applied(ctx, r)
	if appliedErr != nil {
		return nil, appliedErr
	}
	endpoints := b.endpoints(client, sp.Name, spec)
	if public != nil {
		endpoints = append([]cpprovider.Endpoint{*public}, endpoints...)
	}
	return &cpprovider.ProvisionResult{ProviderRef: r.Scope.Key(), ServiceRefs: map[string]string{sp.Name: r.Current.ID}, Endpoints: endpoints}, nil
}

func (b *firecrackerBackend) endpoints(client *fc.Client, name string, s fc.Spec) []cpprovider.Endpoint {
	if b.unpublished {
		s.PublishedPort = 0
	}
	return firecrackerEndpoints(client, name, s)
}
func firecrackerEndpoints(client *fc.Client, name string, s fc.Spec) []cpprovider.Endpoint {
	if s.PublishedPort == 0 {
		if s.Port == 0 {
			return nil
		}
		return []cpprovider.Endpoint{{ServiceName: name, URL: fmt.Sprintf("http://%s.loomplane:%d", s.Service, s.Port), Port: s.Port, Protocol: "http"}}
	}
	endpoint, err := url.Parse(client.Endpoint)
	if err != nil {
		return nil
	}
	return []cpprovider.Endpoint{{ServiceName: name, URL: "http://" + net.JoinHostPort(endpoint.Hostname(), strconv.Itoa(s.PublishedPort)), Port: s.Port, Protocol: "http", Public: true}}
}
func readFirecracker(r fc.Record) fc.Request {
	q := fc.Request{Scope: r.Scope, RuntimeIdentity: r.RuntimeIdentity, Authority: fc.Authority{Generation: r.Generation, Operation: r.Operation, Expires: time.Now().Add(time.Minute)}}
	if r.Current != nil {
		q.Spec = r.Current.Spec
	}
	return q
}
func (b *firecrackerBackend) Status(ctx context.Context, iid id.ID, labels map[string]string) (*cpprovider.InstanceStatus, error) {
	out, err := b.status(ctx, iid, labels)
	return out, b.fail(ctx, "status", err)
}

func (b *firecrackerBackend) status(ctx context.Context, iid id.ID, labels map[string]string) (*cpprovider.InstanceStatus, error) {
	c, scope, err := b.session(ctx, iid, labels)
	if err != nil {
		return nil, err
	}
	if c == nil {
		// Nothing runs it on any host now.
		return &cpprovider.InstanceStatus{State: cpprovider.StateStopped}, nil
	}
	defer c.Close()
	r, err := c.Inspect(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := &cpprovider.InstanceStatus{State: cpprovider.StateStarting}
	if r.State == "stopped" || r.State == "removed" {
		out.State = cpprovider.StateStopped
		return out, nil
	}
	if r.Current == nil {
		return out, nil
	}
	status, err := c.Guest(ctx, readFirecracker(r), "status", nil)
	if err != nil {
		return nil, err
	}
	out.Ready = status.Ready
	if out.Ready {
		out.State = cpprovider.StateRunning
	} else {
		out.State = cpprovider.StateFailed
	}
	out.Endpoints = b.endpoints(c, labels[owned.LabelService], r.Current.Spec)
	return out, nil
}
func (b *firecrackerBackend) mutate(ctx context.Context, iid id.ID, labels map[string]string, action string) error {
	act := func(c *fc.Client, scope fc.Scope) error { return b.mutateOn(ctx, c, scope, labels, action) }
	if b.effect != nil {
		return b.fail(ctx, action, b.effect(ctx, iid, labels, action, act))
	}
	c, scope, err := b.session(ctx, iid, labels)
	if err != nil {
		return b.fail(ctx, action, err)
	}
	defer c.Close()
	return b.fail(ctx, action, act(c, scope))
}

// mutateOn stops or removes what runs under scope on c's host.
func (b *firecrackerBackend) mutateOn(ctx context.Context, c *fc.Client, scope fc.Scope, labels map[string]string, action string) error {
	r, err := c.Inspect(ctx, scope)
	if errors.Is(err, fc.ErrMissing) && action == "remove" {
		return nil
	}
	if err != nil {
		return err
	}
	if r.State == "removed" && action == "remove" {
		return nil
	}
	q := readFirecracker(r)
	if q.Spec.Kind == "" && r.Pending != nil {
		q.Spec = r.Pending.Spec
	}
	if b.target.FirecrackerAuthority == nil {
		return fc.ErrUnavailable
	}
	q.Authority, err = b.target.FirecrackerAuthority.IssueFirecracker(ctx, q.Scope, action+"/"+labels[owned.LabelRevision], firecrackerDigest(q.Spec))
	if err != nil {
		return err
	}
	_, err = c.Mutate(ctx, q, action)
	return err
}
func (b *firecrackerBackend) Remove(ctx context.Context, iid id.ID, labels map[string]string) error {
	return b.mutate(ctx, iid, labels, "remove")
}
func (b *firecrackerBackend) SetRunning(ctx context.Context, iid id.ID, labels map[string]string, running bool) error {
	inst, err := b.target.Ctrl.Instances.Get(ctx, iid)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, firecrackerManualKey{}, firecrackerManual{State: string(inst.State), Updated: inst.UpdatedAt})
	if !running {
		return b.mutate(ctx, iid, labels, "stop")
	}
	if len(inst.Services) != 1 {
		return errors.New("firecracker instance needs one service")
	}
	_, err = b.Apply(ctx, iid, inst.Services[0], labels)
	return err
}
func (*firecrackerBackend) Scale(context.Context, id.ID, map[string]string, cpprovider.ResourceSpec) error {
	return ctrlplane.ErrProviderUnavail
}

func (b *firecrackerBackend) Logs(ctx context.Context, iid id.ID, labels map[string]string, _ cpprovider.LogOptions) (io.ReadCloser, error) {
	out, err := b.logs(ctx, iid, labels)
	return out, b.fail(ctx, "logs", err)
}

func (b *firecrackerBackend) logs(ctx context.Context, iid id.ID, labels map[string]string) (io.ReadCloser, error) {
	client, scope, err := b.session(ctx, iid, labels)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	defer client.Close()
	record, err := client.Inspect(ctx, scope)
	if err != nil {
		return nil, err
	}
	status, err := client.Guest(ctx, readFirecracker(record), "logs", nil)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(status.Output)), nil
}
