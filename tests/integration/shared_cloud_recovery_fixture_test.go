//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	apiclient "github.com/xraph/loomplane/pkg/client"
)

type recoveryFixture struct {
	t                                              *testing.T
	c                                              *client
	db                                             *pgdriver.PgDB
	sql                                            *pgx.Conn
	state                                          combinedState
	directory, org, project, environment, variable string
	private                                        []byte
	cloud                                          cloudprofiles.Profile
	target                                         cloudprofiles.Target
	connections                                    map[string]connections.Connection
	servers                                        map[string]string
	ssh                                            *ssh.Client
	ledger                                         map[string]any
	route                                          *cloudinfra.RouteOperation
	marker                                         string
	manifest                                       string
	revision                                       int
	cfg                                            config.Config
}

func (f *recoveryFixture) record(key string, value any) {
	f.t.Helper()
	f.ledger[key] = value
	raw, err := json.MarshalIndent(f.ledger, "", "  ")
	if err != nil {
		f.t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.directory, "recovery-ledger.json"), raw, 0600); err != nil {
		f.t.Fatal(err)
	}
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	statePath := os.Getenv("LOOMPLANE_SHARED_CLOUD_STATE")
	if statePath == "" {
		t.Skip("run tests/acceptance/shared-clouds/run.py --suite recovery")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	f := &recoveryFixture{t: t, directory: filepath.Dir(statePath), connections: map[string]connections.Connection{}, servers: map[string]string{}, ledger: map[string]any{}}
	if err = json.Unmarshal(raw, &f.state); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.state.RunID, "lp-shared-") || len(f.state.Hosts) != 2 {
		t.Fatal("invalid owned fixture")
	}
	f.private, err = os.ReadFile(filepath.Join(f.directory, "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, running, c, org, project := cloudAPISetup(t)
	f.c, f.org, f.project = c, org, project
	f.cfg = cfg
	db, err := forge.InjectType[*grove.DB](running.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	f.db = pgdriver.Unwrap(db)
	f.sql, err = pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.sql.Close(context.Background()) })
	f.record("identity", map[string]any{"run": f.state.RunID, "org": org, "project": project, "database": f.sql.Config().Database, "hosts": f.state.Hosts})
	f.ssh = combinedSSH(t, f.state.Hosts["host1"], f.private)
	t.Cleanup(func() {
		f.t = t
		count := func() int {
			var buf bytes.Buffer
			if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
				t.Fatal(err)
			}
			return strings.Count(buf.String(), "github.com/xraph/ctrlplane/metrics.(*service).runPoller(")
		}
		before := count()
		running.stop()
		f.record("metrics_pollers_before_after", []int{before, count()})
		f.cleanupRelay()
	})
	f.startRelay()
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+project+"/clouds", cloudprofiles.CreateInput{Name: "Recovery fixture"}, &f.cloud)
	for _, role := range []string{"host1", "host2"} {
		host := f.state.Hosts[role]
		settings := map[string]string{"host": host.Address, "advertise_host": host.Address, "user": "root", "host_key": host.HostKey}
		if role == "host1" {
			settings["docker_socket"] = "/tmp/loomplane-recovery-relay.sock"
		}
		var connection connections.Connection
		c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{"cloud": "server", "name": role, "credential": string(f.private), "settings": settings}, &connection)
		if connection.Status != "ready" {
			t.Fatal("connection is not ready", connection.Status)
		}
		f.connections[role] = connection
		var op cloudinfra.Operation
		base := "/api/v1/clouds/" + f.cloud.ID
		c.must(http.StatusOK, http.MethodPost, base+"/servers/preview", cloudinfra.ServerInput{Kind: "adopt", Name: role, ConnectionID: connection.ID, CloudRevision: f.cloud.Revision, ConfirmAdoption: true}, &op)
		f.servers[role] = op.ServerID
		f.record("servers", f.servers)
		c.must(http.StatusAccepted, http.MethodPost, base+"/infrastructure-operations/"+op.ID+"/apply", cloudinfra.ApplyInput{Review: op.Review}, &op)
		recoveryWait(t, time.Minute, func() bool {
			c.must(http.StatusOK, http.MethodGet, base+"/infrastructure-operations/"+op.ID, nil, &op)
			if op.Status == "failed" {
				t.Fatalf("adopt: %+v", op)
			}
			return op.Status == "succeeded"
		})
	}
	analyze(t, cfg, c, project)
	base := "/api/v1/projects/" + project
	var envs struct{ Environments []struct{ ID, Name string } }
	c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	for _, env := range envs.Environments {
		if env.Name == "production" {
			f.environment = env.ID
		}
	}
	c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+f.cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "direct", Provider: "server", Runtime: "managed", ConnectionID: f.connections["host1"].ID}, &f.target)
	var preview cloudprofiles.Preview
	c.must(http.StatusOK, http.MethodPost, base+"/environments/"+f.environment+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: f.cloud.ID}, &preview)
	waitCloudAPI(t, c, base, applyCloudAPI(t, c, base, &preview, "recovery-assignment").ID)
	manifest := "version: 1\nservices:\n  web:\n    source: node:apps/web\n    cloud_target: direct\n    public: false\n    port: 80\n    health_path: /\n    size: {cpu: 0.2, memory: 128}\n    build: {image: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab}\nignore:\n  resources: [postgres, redis]\n"
	f.manifest = manifest
	c.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": manifest}, nil)
	var variable struct{ ID string }
	c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "WHOAMI_NAME", "value": f.state.RunID + "-initial", "environment": "production"}, &variable)
	f.variable = variable.ID
	f.marker = f.state.RunID + "-initial"
	initial := f.plan()
	f.approve(initial)
	if result := f.deployment(initial); result.Status != "succeeded" {
		t.Fatalf("initial direct release: %+v", result)
	}
	f.record("initial_container", f.container())
	return f
}

func recoveryWait(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("recovery observation timed out")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func (f *recoveryFixture) plan() planBody {
	f.t.Helper()
	f.revision++
	manifest := strings.Replace(f.manifest, "health_path: /", fmt.Sprintf("health_path: /release-%d", f.revision), 1)
	f.c.must(http.StatusCreated, http.MethodPut, "/api/v1/projects/"+f.project+"/manifest", map[string]string{"yaml": manifest}, nil)
	var plan planBody
	f.c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects/"+f.project+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(f.t, f.c, &plan)
	if !plan.Executable {
		f.t.Fatalf("unexecutable recovery plan: %+v", plan)
	}
	persisted, err := planning.NewPgStore(f.db).GetPlan(f.t.Context(), f.org, plan.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	f.record("actions_"+plan.ID, persisted.Actions)
	f.record("latest_plan", plan.ID)
	return plan
}
func (f *recoveryFixture) approve(plan planBody) {
	f.t.Helper()
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
}
func (f *recoveryFixture) deployment(plan planBody) deploymentBody {
	f.t.Helper()
	var dep deploymentBody
	recoveryWait(f.t, 90*time.Second, func() bool {
		status := f.c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &dep)
		return status == http.StatusOK && (dep.Status == "succeeded" || dep.Status == "failed" || dep.Status == "canceled")
	})
	f.record("deployment_"+plan.ID, dep)
	return dep
}
func (f *recoveryFixture) container() string {
	f.t.Helper()
	var id string
	// ServiceRefs records initial creation. Updates publish a ready receipt tied
	// to the latest exact successful controller deployment, as route admission requires.
	if err := f.sql.QueryRow(f.t.Context(), `SELECT CASE WHEN d.id IS NULL THEN i.service_refs->>'main'
 WHEN d.state='succeeded' AND d.release_id=i.labels->>$4 AND d.release_id=i.labels->>$5 THEN i.labels->>$6 END
 FROM cp_instances i JOIN loomplane_bindings b ON b.external_id=i.id AND b.org_id=i.tenant_id
 LEFT JOIN LATERAL (SELECT id,release_id,state FROM cp_deployments WHERE tenant_id=i.tenant_id AND instance_id=i.id ORDER BY created_at DESC,id DESC LIMIT 1) d ON true
 WHERE b.org_id=$1 AND b.project_id=$2 AND b.environment_id=$3 AND b.name='web' AND i.state='running'`,
		f.org, f.project, f.environment, owned.LabelReceiptRelease, owned.LabelRevision, owned.LabelReceiptServicePrefix+"main").Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	if id == "" {
		f.t.Fatal("missing exact ready runtime container receipt")
	}
	return id
}
func (f *recoveryFixture) physical() string {
	return combinedExec(f.t, f.ssh, nil, "docker ps -aq --no-trunc | xargs -r docker inspect --format '{{.Id}} {{.State.Running}} {{.RestartCount}}' | sort")
}

func (f *recoveryFixture) verifyDirectContainer(receipt, deployment string) string {
	t := f.t
	t.Helper()
	id := f.container()
	var instance, release, connection string
	if err := f.sql.QueryRow(t.Context(), `SELECT i.id,i.labels->>$4,b.connection_id
 FROM cp_instances i JOIN loomplane_bindings b ON b.external_id=i.id AND b.org_id=i.tenant_id
 WHERE b.org_id=$1 AND b.project_id=$2 AND b.environment_id=$3 AND b.name='web'`,
		f.org, f.project, f.environment, owned.LabelReceiptRelease).Scan(&instance, &release, &connection); err != nil {
		t.Fatal(err)
	}
	if instance == "" || release == "" || connection != f.connections["host1"].ID {
		t.Fatal("missing exact direct instance/release identity")
	}
	daemon := combinedExec(t, f.ssh, nil, "docker info --format '{{.ID}}'")
	if daemon != f.state.Hosts["host1"].Daemon {
		t.Fatal("direct inspection reached another Docker daemon")
	}
	var inspected []struct {
		ID     string `json:"Id"`
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
	}
	if err := json.Unmarshal([]byte(combinedExec(t, f.ssh, nil, "docker inspect "+combinedQuote(id))), &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected) != 1 || inspected[0].ID != id || !inspected[0].State.Running {
		t.Fatal("ready receipt lacks its exact running physical container", inspected)
	}
	for key, expected := range map[string]string{owned.LabelOrg: f.org, owned.LabelProject: f.project, owned.LabelEnvironment: f.environment, owned.LabelInstance: instance, owned.LabelRevision: release, owned.LabelConnection: connection} {
		if inspected[0].Config.Labels[key] != expected {
			t.Fatalf("physical container %s has wrong %s", id, key)
		}
	}
	f.record(receipt, map[string]any{"daemon": daemon, "deployment": deployment, "instance": instance, "release": release, "inspection": inspected[0]})
	return id
}

// The relay records only the selected Docker stop path and peer PID. Every byte
// still reaches the real daemon; it never waits on a test barrier or invents a reply.
const recoveryRelay = `import os,socket,select,threading,json,re,struct,signal
observation_errors=[]
observation_lock=threading.Lock()
def observation_failed(error):
 with observation_lock:observation_errors.append(type(error).__name__)
def observer(pid):
 pending=b'';remaining=0;inspect=True
 def record(head):
  line=head.split(b'\r\n',1)[0].decode('ascii')
  match=re.fullmatch(r'POST (/containers/([a-f0-9]{64})/stop\?t=10) HTTP/1\.[01]',line)
  if match:
   ancestry=[];current=pid
   while current>1:
    with open('/proc/'+str(current)+'/cmdline','rb') as source:command=source.read().replace(b'\0',b' ').decode()
    with open('/proc/'+str(current)+'/status') as source:status=source.read()
    parent=int(re.search(r'^PPid:\s+(\d+)',status,re.M).group(1))
    ancestry.append({'pid':current,'parent':parent,'command':command.split(' ',1)[0]})
    if 'sshd' in command.split(' ',1)[0]:break
    current=parent
   receipt={'method':'POST','path':match.group(1),'container':match.group(2),'helper':pid,'ancestry':ancestry}
   fd=os.open('/tmp/loomplane-recovery-stop.jsonl',os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600)
   try:os.write(fd,(json.dumps(receipt)+'\n').encode())
   finally:os.close(fd)
 def observe(data):
  nonlocal pending,remaining,inspect
  if not inspect:return
  pending+=data
  while pending:
   if remaining:
    size=min(remaining,len(pending));pending=pending[size:];remaining-=size
    continue
   end=pending.find(b'\r\n\r\n')
   if end<0:
    if len(pending)>1048576:inspect=False;pending=b''
    return
   head=pending[:end+4];pending=pending[end+4:]
   headers=head.lower()
   if b'transfer-encoding: chunked' in headers or b'upgrade:' in headers:
    inspect=False;pending=b'';return
   length=re.search(rb'\r\ncontent-length:\s*(\d+)',headers)
   remaining=int(length.group(1)) if length else 0
   record(head)
 return observe
def forward(client,upstream,observe):
 try:
  readers=[client,upstream]
  while readers:
   ready,_,_=select.select(readers,[],[],60)
   if not ready:continue
   for source in ready:
    destination=upstream if source is client else client
    chunk=source.recv(65536)
    if chunk:
     destination.sendall(chunk)
     if source is client and observe is not None:
      try:observe(chunk)
      except Exception as error:
       observation_failed(error);observe=None
    else:
     readers.remove(source)
     destination.shutdown(socket.SHUT_WR)
 except OSError:pass
 finally:client.close();upstream.close()
def handle(client):
 upstream=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
 try:
  upstream.connect('/var/run/docker.sock')
  observe=None
  try:
   pid,uid,gid=struct.unpack('3i',client.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))
   observe=observer(pid)
  except Exception as error:observation_failed(error)
  forward(client,upstream,observe)
 except OSError:pass
 finally:client.close();upstream.close()
def observation_summary():
 with observation_lock:return {'error_count':len(observation_errors),'errors':list(observation_errors)}
def finish(signum,frame):
 # A missing or unreadable final summary also fails qualification after teardown.
 with open('/tmp/loomplane-recovery-observation.json','w') as target:json.dump(observation_summary(),target)
 raise SystemExit(0)
if __name__=='__main__':
 path='/tmp/loomplane-recovery-relay.sock'
 server=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
 server.bind(path);server.listen(64)
 open('/tmp/loomplane-recovery-relay.pid','w').write(str(os.getpid()))
 signal.signal(signal.SIGTERM,finish)
 while True:
  client,_=server.accept()
  threading.Thread(target=handle,args=(client,),daemon=True).start()
`

func (f *recoveryFixture) startRelay() {
	t := f.t
	combinedExec(t, f.ssh, []byte(recoveryRelay), "umask 077; cat > /tmp/loomplane-recovery-relay.py")
	combinedExec(t, f.ssh, nil, "nohup python3 /tmp/loomplane-recovery-relay.py >/tmp/loomplane-recovery-relay.log 2>&1 </dev/null &")
	recoveryWait(t, 5*time.Second, func() bool {
		return combinedExec(t, f.ssh, nil, "test -S /tmp/loomplane-recovery-relay.sock && echo ready || true") == "ready"
	})
	f.record("relay", map[string]string{"pid": combinedExec(t, f.ssh, nil, "cat /tmp/loomplane-recovery-relay.pid"), "socket": "/tmp/loomplane-recovery-relay.sock", "daemon": f.state.Hosts["host1"].Daemon})
}

func (f *recoveryFixture) cleanupRelay() {
	// The app has stopped before this exact PID/socket cleanup runs.
	script := `import os,signal,time,json
p='/tmp/loomplane-recovery-relay.pid'
if os.path.exists(p):
 pid=int(open(p).read())
 command=open('/proc/'+str(pid)+'/cmdline','rb').read()
 assert command==b'python3\0/tmp/loomplane-recovery-relay.py\0'
 os.kill(pid,signal.SIGTERM)
 for _ in range(100):
  if not os.path.exists('/proc/'+str(pid)) or '\nState:\tZ' in open('/proc/'+str(pid)+'/status').read():break
  time.sleep(.02)
 else:raise RuntimeError('owned relay process did not exit')
try:
 with open('/tmp/loomplane-recovery-observation.json') as source:summary=json.load(source)
 assert isinstance(summary['errors'],list) and summary['error_count']==len(summary['errors'])
except Exception as error:summary={'error_count':1,'errors':['summary_'+type(error).__name__]}
for p in ('/tmp/loomplane-recovery-relay.sock','/tmp/loomplane-recovery-relay.pid','/tmp/loomplane-recovery-relay.py','/tmp/loomplane-recovery-observation.json','/var/run/loomplane-recovery-drift.sock'):
 if os.path.lexists(p):os.unlink(p)
 assert not os.path.lexists(p)
print(json.dumps({'cleanup':'exact relay process and sockets removed','observation':summary}))`
	var receipt struct {
		Cleanup     string `json:"cleanup"`
		Observation struct {
			ErrorCount int      `json:"error_count"`
			Errors     []string `json:"errors"`
		} `json:"observation"`
	}
	if err := json.Unmarshal([]byte(combinedExec(f.t, f.ssh, []byte(script), "python3 -")), &receipt); err != nil {
		f.t.Fatal(err)
	}
	f.record("relay_cleanup", receipt.Cleanup)
	f.record("relay_observation", receipt.Observation)
	if receipt.Observation.ErrorCount != 0 || len(receipt.Observation.Errors) != 0 {
		f.t.Errorf("relay observation failed without interrupting forwarding: %+v", receipt.Observation)
	}
}

type recoveryResponse struct {
	Status int
	Body   string
	Err    error
}

func recoveryHTTP(c *client, method, path string, input any) recoveryResponse {
	raw, err := json.Marshal(input)
	if err != nil {
		return recoveryResponse{Err: err}
	}
	ctx, cancel := context.WithTimeout(c.context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return recoveryResponse{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return recoveryResponse{Err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return recoveryResponse{Status: response.StatusCode, Body: string(body), Err: err}
}

func (f *recoveryFixture) ingress(role string) {
	t := f.t
	connection := f.connections[role]
	epoch, err := cloudinfra.NewPgStore(f.db).BeginServerIngress(t.Context(), f.org, f.servers[role])
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := server.ParseConfig(f.private, connection.Details)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Details["docker_id"] = f.state.Hosts[role].Daemon
	remote, err := server.Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	remote.RequireFence = true
	ctx := server.WithEffectFence(t.Context(), server.EffectFence{Operation: f.org + "/ingress/" + f.servers[role], Generation: epoch.Generation})
	proxy, err := server.EnsureIngress(ctx, remote, f.org, f.servers[role])
	if err != nil {
		t.Fatal(err)
	}
	sshClient := combinedSSH(t, f.state.Hosts[role], f.private)
	if combinedExec(t, sshClient, nil, "docker inspect --format '{{.Config.Image}}' "+combinedQuote(proxy.ContainerID)) != server.IngressImage {
		t.Fatal("ingress pin changed")
	}
	f.record("proxy_"+role, proxy.ContainerID)
	trust, err := os.ReadFile(filepath.Join(f.directory, "transport.pem"))
	if err != nil {
		t.Fatal(err)
	}
	combinedExec(t, sshClient, trust, "docker exec -i "+combinedQuote(proxy.ContainerID)+" sh -c 'cat >> /etc/ssl/certs/ca-certificates.crt'")
	combinedExec(t, sshClient, nil, "docker restart "+combinedQuote(proxy.ContainerID))
}

func (f *recoveryFixture) routeReview() *cloudinfra.RouteOperation {
	f.t.Helper()
	if f.route != nil {
		return f.route
	}
	f.routedProject()
	f.ingress("host2")
	var zone cloudinfra.Zone
	f.c.must(http.StatusCreated, http.MethodPost, "/api/v1/dns-zones", cloudinfra.ZoneInput{Name: f.state.RunID + ".test", Mode: "external", ProjectIDs: []string{f.project}}, &zone)
	combinedDNS(f.t, f.state.Addresses["gateway"], zone.ProofName, "TXT", zone.ProofValue)
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/verify", nil, &zone)
	hostname := "recovery." + zone.Name
	combinedDNS(f.t, f.state.Addresses["gateway"], hostname, "A", f.state.Hosts["host2"].Address)
	var route cloudinfra.RouteOperation
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/projects/"+f.project+"/domain-routes/preview", cloudinfra.RouteInput{ZoneID: zone.ID, CloudID: f.cloud.ID, EnvironmentID: f.environment, Service: "web", Hostname: hostname, Port: 80, ExternalTTLSeconds: combinedExternalTTLSeconds}, &route)
	f.route = &route
	f.record("route", route)
	return f.route
}
func (f *recoveryFixture) routeReady() {
	t := f.t
	route := f.routeReview()
	path := "/api/v1/projects/" + f.project + "/route-operations/" + route.ID
	if route.Status == "preview" {
		f.c.must(http.StatusAccepted, http.MethodPost, path+"/apply", cloudinfra.ApplyInput{Review: route.Review}, route)
	}
	recoveryWait(t, 90*time.Second, func() bool {
		f.c.must(http.StatusOK, http.MethodGet, path, nil, route)
		if route.Status == "failed" {
			f.c.must(http.StatusAccepted, http.MethodPost, path+"/retry", nil, route)
		}
		return route.Status == "succeeded"
	})
	if err := combinedHTTPS(t.Context(), route.Route.Input.Hostname, f.state.Hosts["host2"].Address, f.marker, f.container()); err != nil {
		t.Fatal(err)
	}
	f.record("ready_route", route)
}

func (f *recoveryFixture) routedProject() {
	t := f.t
	// Routes need an accepted pool placement with a recorded server. Keep the
	// direct workload on host1 and give this second project host2's owned pool.
	var pool cloudinfra.Pool
	f.c.must(http.StatusOK, http.MethodPost, "/api/v1/clouds/"+f.cloud.ID+"/pools", cloudinfra.PoolInput{Name: "Routed pool", Members: []cloudinfra.PoolMember{{ServerID: f.servers["host2"], CPUMillis: 2000, MemoryBytes: 1 << 30}}}, &pool)
	var project struct{ ID string }
	f.c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Routed recovery project", "repository_url": "https://github.com/acme/mono"}, &project)
	f.record("direct_project", f.project)
	f.project = project.ID
	f.record("routed_project", f.project)
	f.c.must(http.StatusOK, http.MethodPut, "/api/v1/clouds/"+f.cloud.ID+"/projects/"+f.project, map[string]any{"target_ids": []string{}}, nil)
	analyze(t, f.cfg, f.c, f.project)
	base := "/api/v1/projects/" + f.project
	var envs struct{ Environments []struct{ ID, Name string } }
	f.c.must(http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	for _, env := range envs.Environments {
		if env.Name == "production" {
			f.environment = env.ID
		}
	}
	f.c.must(http.StatusCreated, http.MethodPost, base+"/clouds/"+f.cloud.ID+"/targets", cloudprofiles.CreateTargetInput{Name: "routed", Provider: "server", Runtime: "server", PoolID: pool.ID}, &f.target)
	var preview cloudprofiles.Preview
	f.c.must(http.StatusOK, http.MethodPost, base+"/environments/"+f.environment+"/cloud/preview", apiclient.EnvironmentCloudInput{CloudID: f.cloud.ID}, &preview)
	waitCloudAPI(t, f.c, base, applyCloudAPI(t, f.c, base, &preview, "routed-assignment").ID)
	f.manifest = strings.Replace(f.manifest, "cloud_target: direct", "cloud_target: routed", 1)
	f.marker = f.state.RunID + "-routed"
	var variable struct{ ID string }
	f.c.must(http.StatusCreated, http.MethodPost, base+"/variables", map[string]any{"key": "WHOAMI_NAME", "value": f.marker, "environment": "production"}, &variable)
	f.variable = variable.ID
	plan := f.plan()
	f.approve(plan)
	if result := f.deployment(plan); result.Status != "succeeded" {
		t.Fatalf("routed release: %+v", result)
	}
	f.record("routed_container", f.container())
}
