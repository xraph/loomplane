//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"

	nats "github.com/nats-io/nats.go"
	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/planning"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

type natsTestDialer struct{ dial func() (net.Conn, error) }

func (d natsTestDialer) Dial(_, _ string) (net.Conn, error) { return d.dial() }

func natsTestConnection(t *testing.T, socket, container, user, password string) (*nats.Conn, error) {
	t.Helper()
	return nats.Connect("nats://127.0.0.1:4222", nats.UserInfo(user, password), nats.NoReconnect(), nats.Timeout(5*time.Second), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}), nats.SetCustomDialer(natsTestDialer{dial: func() (net.Conn, error) {
		local, remote := net.Pipe()
		cmd := exec.CommandContext(t.Context(), "docker", "--host", "unix://"+socket, "exec", "-i", container, "nc", "127.0.0.1", "4222")
		input, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		cmd.Stdout = remote
		cmd.Stderr = io.Discard
		if err = cmd.Start(); err != nil {
			local.Close()
			remote.Close()
			return nil, err
		}
		go func() { _, _ = io.Copy(input, remote); _ = input.Close() }()
		go func() { _ = cmd.Wait(); _ = remote.Close() }()
		return local, nil
	}}))
}
func natsTestPassword(t *testing.T, socket, container, user string) string {
	t.Helper()
	raw := fixtureCommand(t, "docker", "--host", "unix://"+socket, "inspect", container, "--format", "{{json .Config.Env}}")
	var env []string
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal("invalid container environment")
	}
	key := "LOOMPLANE_APP_PASSWORD="
	if user == "admin" {
		key = "LOOMPLANE_NATS_ADMIN_PASSWORD="
	}
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, key); ok {
			return value
		}
	}
	t.Fatal("missing NATS credential")
	return ""
}
func TestAcceptanceNATSCatalogRecovery(t *testing.T) {
	f := catalogServer(t)
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(f.cli.must("services", "create", "NATS acceptance", "--kind", "nats", "--target", f.connection, "--json")), &plan); err != nil || !plan.Executable {
		t.Fatalf("NATS plan: %+v %v", plan, err)
	}
	var sourceVolume string
	t.Cleanup(func() {
		cleanupCatalogProject(t, f.socket, plan.ProjectID)
		if sourceVolume != "" {
			fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "volume", "rm", "--force", sourceVolume)
		}
	})
	f.cli.must("approve", plan.ID, "--yes")
	var bindings struct{ Bindings []planning.Binding }
	f.client.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+plan.ProjectID+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 {
		t.Fatal("NATS binding missing")
	}
	iid, err := id.Parse(bindings.Bindings[0].ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	source := server.ContainerName(iid)
	sourceVolume = server.VolumeName(iid, "data")
	binding := bindings.Bindings[0]
	endpoint, endpointErr := f.resources.Get(t.Context(), deployments.ResourceKey(binding.OrgID, binding.EnvironmentID, binding.Name, deployments.ResourceURL))
	if endpointErr != nil || !strings.HasPrefix(string(endpoint), "nats://app:") || !strings.Contains(string(endpoint), "@"+source+":4222") {
		t.Fatal("missing native NATS application binding")
	}
	var readKey keyBody
	f.client.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "NATS recovery reader", "access": "read"}, &readKey)
	observer := loomctl{t: t, url: f.running.srv.URL, key: readKey.Secret, config: filepath.Join(t.TempDir(), "reader.json")}
	if _, denied := observer.run("", "backups", "create", plan.ProjectID, "--request-id", "denied"); !errors.Is(denied, lpclient.ErrForbidden) {
		t.Fatalf("read-only backup admission: %v", denied)
	}

	connect := func(container, user string) *nats.Conn {
		t.Helper()
		nc, e := natsTestConnection(t, f.socket, container, user, natsTestPassword(t, f.socket, container, user))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(nc.Close)
		return nc
	}
	nc := connect(source, "app")
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = js.AddStream(&nats.StreamConfig{Name: "ORDERS", Subjects: []string{"orders.>"}, Storage: nats.FileStorage, Replicas: 1, MaxBytes: 16 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err = js.AddStream(&nats.StreamConfig{Name: "PUSH", Subjects: []string{"push.>"}, Storage: nats.FileStorage, Replicas: 1, Retention: nats.InterestPolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err = js.AddConsumer("PUSH", &nats.ConsumerConfig{Durable: "push-reader", DeliverSubject: "_INBOX.recovered-push", AckPolicy: nats.AckNonePolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err = js.Publish("push.created", []byte("must remain available")); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"acknowledged", "pending"} {
		if _, err = js.Publish("orders.created", []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := js.PullSubscribe("orders.>", "worker", nats.ManualAck(), nats.AckExplicit())
	if err != nil {
		t.Fatal(err)
	}
	messages, err := sub.Fetch(1, nats.MaxWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = messages[0].AckSync(); err != nil {
		t.Fatal(err)
	}
	nc.Close()
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "restart", source)
	nc = connect(source, "app")
	js, err = nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	state, err := js.ConsumerInfo("ORDERS", "worker")
	if err != nil || state.AckFloor.Stream != 1 || state.NumPending != 1 {
		t.Fatalf("persistence failed: %+v %v", state, err)
	}
	var backup lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(f.cli.must("backups", "create", plan.ProjectID, "--request-id", "nats-backup", "--wait", "--json")), &backup); err != nil || backup.Status != "succeeded" {
		t.Fatalf("backup: %+v %v", backup, err)
	}
	observer.must("backups", "show", backup.ID, "--json")
	if _, denied := observer.run("", "restores", "create", backup.ID, "--request-id", "denied"); !errors.Is(denied, lpclient.ErrForbidden) {
		t.Fatalf("read-only restore admission: %v", denied)
	}
	if err = js.PurgeStream("ORDERS"); err != nil {
		t.Fatal(err)
	}
	var restored lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(f.cli.must("restores", "create", backup.ID, "--request-id", "nats-restore", "--wait", "--json")), &restored); err != nil || restored.Status != "succeeded" || restored.VerifiedAt == nil {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	copyID, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	copyName := server.ContainerName(copyID)
	copyNC := connect(copyName, "admin")
	copyJS, err := copyNC.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	info, err := copyJS.StreamInfo("ORDERS")
	if err != nil || info.State.Msgs != 2 {
		t.Fatalf("snapshot messages: %+v %v", info, err)
	}
	state, err = copyJS.ConsumerInfo("ORDERS", "worker")
	if err != nil || state.AckFloor.Stream != 1 || state.NumPending != 1 {
		t.Fatalf("consumer acknowledgement lost: %+v %v", state, err)
	}
	copied, err := copyJS.PullSubscribe("orders.>", "worker", nats.Bind("ORDERS", "worker"))
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := copied.Fetch(1, nats.MaxWait(5*time.Second))
	if err != nil || string(remaining[0].Data) != "pending" {
		t.Fatalf("remaining message: %v", err)
	}
	live, err := js.StreamInfo("ORDERS")
	if err != nil || live.State.Msgs != 0 {
		t.Fatalf("restore changed source: %+v %v", live, err)
	}
	oldPassword := natsTestPassword(t, f.socket, source, "app")
	if rejected, e := natsTestConnection(t, f.socket, copyName, "app", oldPassword); e == nil {
		rejected.Close()
		t.Fatal("copy accepts original credentials")
	}
	reader := connect(copyName, "app")
	readerJS, err := reader.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readerJS.GetMsg("ORDERS", 2); err != nil {
		t.Fatal("recovery reader cannot inspect message", err)
	}
	pushSub, err := reader.SubscribeSync("_INBOX.recovered-push")
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.FlushTimeout(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if _, deliveryErr := pushSub.NextMsg(time.Second); !errors.Is(deliveryErr, nats.ErrTimeout) {
		t.Fatalf("inspection activated a restored push consumer: %v", deliveryErr)
	}
	pushState, err := copyJS.ConsumerInfo("PUSH", "push-reader")
	if err != nil || pushState.Delivered.Consumer != 0 || pushState.AckFloor.Consumer != 0 || pushState.NumPending != 1 {
		t.Fatalf("inspection advanced push state: %+v %v", pushState, err)
	}
	pushStream, err := copyJS.StreamInfo("PUSH")
	if err != nil || pushStream.State.Msgs != 1 {
		t.Fatalf("inspection removed push messages: %+v %v", pushStream, err)
	}
	if _, err = readerJS.Publish("orders.created", []byte("forbidden"), nats.AckWait(time.Second)); err == nil {
		t.Fatal("recovery reader can publish")
	}
	var networkInfo struct {
		HostConfig      struct{ PortBindings map[string]json.RawMessage }
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
	}
	raw := fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "inspect", copyName, "--format", "{{json .}}")
	if json.Unmarshal([]byte(raw), &networkInfo) != nil || len(networkInfo.HostConfig.PortBindings) != 0 || len(networkInfo.NetworkSettings.Networks) != 1 {
		t.Fatal("recovery published or joined extra networks")
	}
	for _, network := range networkInfo.NetworkSettings.Networks {
		if fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "network", "inspect", network.NetworkID, "--format", "{{.Internal}}") != "true" {
			t.Fatal("recovery network permits egress")
		}
	}
	f.cli.must("backups", "policy", plan.ProjectID, "--resource", "database", "--every-hours", "6")
	f.cli.must("backups", "policy", plan.ProjectID, "--resource", "database", "--every-hours", "6", "--disable")
	reader.Close()
	copyNC.Close()
	f.cli.must("restores", "delete", restored.ID, "--yes")
	if volumes := fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+copyID.String()); volumes != "" {
		t.Fatal("recovery volume survived cleanup")
	}
	holdCatalogBrowser(t, f.running.srv.URL, plan.ProjectID, f.connection)
	// Removing compute retains source storage and recovery history.
	nc.Close()
	base := "/api/v1/projects/" + plan.ProjectID
	f.client.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nresources: {}\n"}, nil)
	var removal planBody
	f.client.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &removal)
	waitPlanned(t, f.client, &removal)
	if !removal.Executable {
		t.Fatal("source removal not executable")
	}
	f.cli.must("approve", removal.ID, "--yes")
	if retained := fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+iid.String()); retained == "" {
		t.Fatal("source removal destroyed retained data")
	}
	var afterRemoval lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(f.cli.must("restores", "create", backup.ID, "--request-id", "after-removal", "--wait", "--json")), &afterRemoval); err != nil || afterRemoval.Status != "succeeded" || afterRemoval.VerifiedAt == nil {
		t.Fatalf("restore after source removal: %+v %v", afterRemoval, err)
	}
	f.cli.must("restores", "delete", afterRemoval.ID, "--yes")
	t.Log("NATS native messages, consumer acknowledgements, persistence, denial, isolated recovery, fresh credentials, cleanup and restore after source removal passed")
}
