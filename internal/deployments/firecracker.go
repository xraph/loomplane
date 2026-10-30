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

type firecrackerBackend struct {
	target          *OwnedTarget
	org, connection string
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
		if sp.Ports[0].Protocol != "" && sp.Ports[0].Protocol != "tcp" {
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
	client, err := b.target.firecrackerSession(ctx, b.org, b.connection, labels)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	spec, err := firecrackerSpec(sp, labels)
	if err != nil {
		return nil, err
	}
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
	q := fc.Request{Scope: fcScope(iid, labels), RuntimeIdentity: client.Details["runtime_identity"], Spec: spec, Values: fc.Values{Env: env}}
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
	return &cpprovider.ProvisionResult{ProviderRef: r.Scope.Key(), ServiceRefs: map[string]string{sp.Name: r.Current.ID}, Endpoints: firecrackerEndpoints(client, sp.Name, spec)}, nil
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
	c, err := b.target.firecrackerSession(ctx, b.org, b.connection, labels)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	r, err := c.Inspect(ctx, fcScope(iid, labels))
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
	out.Endpoints = firecrackerEndpoints(c, labels[owned.LabelService], r.Current.Spec)
	return out, nil
}
func (b *firecrackerBackend) mutate(ctx context.Context, iid id.ID, labels map[string]string, action string) error {
	c, err := b.target.firecrackerSession(ctx, b.org, b.connection, labels)
	if err != nil {
		return err
	}
	defer c.Close()
	r, err := c.Inspect(ctx, fcScope(iid, labels))
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
	client, err := b.target.firecrackerSession(ctx, b.org, b.connection, labels)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	record, err := client.Inspect(ctx, fcScope(iid, labels))
	if err != nil {
		return nil, err
	}
	status, err := client.Guest(ctx, readFirecracker(record), "logs", nil)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(status.Output)), nil
}
