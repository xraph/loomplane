//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	lpclient "github.com/xraph/loomplane/pkg/client"
)

func rabbitPassword(t *testing.T, socket, container, user string) string {
	t.Helper()
	var env []string
	if err := json.Unmarshal([]byte(fixtureCommand(t, "docker", "--host", "unix://"+socket, "inspect", container, "--format", "{{json .Config.Env}}")), &env); err != nil {
		t.Fatal(err)
	}
	key := "LOOMPLANE_APP_PASSWORD="
	if user == "admin" {
		key = "RABBITMQ_DEFAULT_PASS="
	}
	for _, v := range env {
		if password, ok := strings.CutPrefix(v, key); ok {
			return password
		}
	}
	t.Fatal("missing RabbitMQ credential")
	return ""
}
func rabbitConnect(t *testing.T, socket, container, user, password string) (*amqp.Connection, error) {
	t.Helper()
	return amqp.DialConfig("amqp://"+url.UserPassword(user, password).String()+"@localhost/%2F", amqp.Config{Dial: func(string, string) (net.Conn, error) {
		local, remote := net.Pipe()
		cmd := exec.CommandContext(t.Context(), "docker", "--host", "unix://"+socket, "exec", "-i", container, "nc", "127.0.0.1", "5672")
		input, err := cmd.StdinPipe()
		if err != nil {
			local.Close()
			remote.Close()
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
		// Bound authentication even when the broker is restarting. The AMQP
		// client clears this deadline once its handshake completes.
		_ = local.SetDeadline(time.Now().Add(10 * time.Second))
		return local, nil
	}})
}
func rabbitChannel(t *testing.T, socket, container, user string) *amqp.Channel {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		c, err := rabbitConnect(t, socket, container, user, rabbitPassword(t, socket, container, user))
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
			ch, e := c.Channel()
			if e != nil {
				t.Fatal(e)
			}
			return ch
		}
		if time.Now().After(deadline) {
			t.Fatal("RabbitMQ connection failed", err)
		}
		time.Sleep(time.Second)
	}
}

func rabbitFailedBackup(t *testing.T, f catalogFixture, project, request string) {
	t.Helper()
	var backup lpclient.ServiceBackup
	if err := json.Unmarshal([]byte(f.cli.must("backups", "create", project, "--request-id", request, "--json")), &backup); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		f.client.must(http.StatusOK, http.MethodGet, "/api/v1/service-backups/"+backup.ID, nil, &backup)
		if backup.Status == "succeeded" {
			t.Fatal("unsupported or injected-failure backup succeeded")
		}
		if backup.Status == "failed" && backup.Attempt == 3 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("backup failure retries did not settle")
}
func TestAcceptanceRabbitMQCatalogRecovery(t *testing.T) {
	f := catalogServer(t)
	var plan lpclient.Plan
	if err := json.Unmarshal([]byte(f.cli.must("services", "create", "RabbitMQ acceptance", "--kind", "rabbitmq", "--target", f.connection, "--json")), &plan); err != nil || !plan.Executable {
		t.Fatalf("RabbitMQ plan: %+v %v", plan, err)
	}
	var sourceVolume string
	t.Cleanup(func() {
		cleanupCatalogProject(t, f.socket, plan.ProjectID)
		if sourceVolume != "" {
			fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "volume", "rm", "--force", sourceVolume)
		}
	})
	f.cli.must("approve", plan.ID, "--yes")
	t.Log("RabbitMQ deployed")
	var bindings struct{ Bindings []planning.Binding }
	f.client.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+plan.ProjectID+"/bindings?environment=production", nil, &bindings)
	if len(bindings.Bindings) != 1 {
		t.Fatal("missing RabbitMQ binding")
	}
	binding := bindings.Bindings[0]
	iid, err := id.Parse(binding.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	source := server.ContainerName(iid)
	sourceVolume = server.VolumeName(iid, "data")
	endpoint, err := f.resources.Get(t.Context(), deployments.ResourceKey(binding.OrgID, binding.EnvironmentID, binding.Name, deployments.ResourceURL))
	if err != nil || !strings.HasPrefix(string(endpoint), "amqp://app:") || !strings.Contains(string(endpoint), "@"+source+":5672/%2F") {
		t.Fatal("missing AMQP binding", err)
	}
	var readKey keyBody
	f.client.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "RabbitMQ recovery reader", "access": "read"}, &readKey)
	observer := loomctl{t: t, url: f.running.srv.URL, key: readKey.Secret, config: filepath.Join(t.TempDir(), "reader.json")}
	if _, denied := observer.run("", "backups", "create", plan.ProjectID, "--request-id", "denied"); !errors.Is(denied, lpclient.ErrForbidden) {
		t.Fatal("reader backup admission", denied)
	}
	ch := rabbitChannel(t, f.socket, source, "app")
	if _, err = ch.QueueDeclare("ephemeral", true, false, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if err = ch.PublishWithContext(t.Context(), "", "ephemeral", false, false, amqp.Publishing{Body: []byte("must-survive-rejection")}); err != nil {
		t.Fatal(err)
	}
	rabbitFailedBackup(t, f, plan.ProjectID, "unsupported")
	if msg, ok, e := ch.Get("ephemeral", true); e != nil || !ok || string(msg.Body) != "must-survive-rejection" {
		t.Fatal("rejected backup disrupted transient data", e)
	}
	if _, err = ch.QueueDelete("ephemeral", false, false, false); err != nil {
		t.Fatal(err)
	}
	if err = ch.Confirm(false); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"classic", "quorum"} {
		if _, err = ch.QueueDeclare(kind, true, false, false, false, amqp.Table{"x-queue-type": kind}); err != nil {
			t.Fatal(err)
		}
		for _, payload := range []string{"acknowledged", "pending", "unacknowledged"} {
			confirmation, e := ch.PublishWithDeferredConfirmWithContext(t.Context(), "", kind, false, false, amqp.Publishing{DeliveryMode: amqp.Persistent, ContentType: "text/plain", Body: []byte(payload)})
			if e != nil {
				t.Fatal(e)
			}
			confirmed, e := confirmation.WaitContext(t.Context())
			if e != nil || !confirmed {
				t.Fatal("publish not confirmed", e)
			}
		}
		msg, ok, e := ch.Get(kind, false)
		if e != nil || !ok || string(msg.Body) != "acknowledged" {
			t.Fatal("missing first message", e)
		}
		if err = msg.Ack(false); err != nil {
			t.Fatal(err)
		}
	}
	_ = ch.Close()
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "exec", "-d", source, "sh", "-c", "exec 9>/tmp/loomplane-rabbitmq-backup.lock; flock -n 9; touch /tmp/loomplane-rabbitmq-lock-held; sleep 300")
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "exec", source, "sh", "-c", "for i in 1 2 3 4 5; do test ! -f /tmp/loomplane-rabbitmq-lock-held || exit 0; sleep 1; done; exit 1")
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "restart", source)
	ch = rabbitChannel(t, f.socket, source, "app")
	for _, kind := range []string{"classic", "quorum"} {
		q, e := ch.QueueDeclarePassive(kind, true, false, false, false, amqp.Table{"x-queue-type": kind})
		if e != nil || q.Messages != 2 {
			t.Fatalf("restart lost %s messages: %+v %v", kind, q, e)
		}
		if _, ok, e := ch.Get(kind, false); e != nil || !ok {
			t.Fatal("unacknowledged fixture", e)
		}
	}
	t.Log("durable classic and quorum messages survived restart")
	t.Log(fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "exec", source, "sh", "-c", "HOME=/var/lib/rabbitmq rabbitmqctl -q list_queues name type messages messages_persistent arguments --formatter json"))
	// Fail after stop_app, then prove the cleanup trap restarts the source.
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "exec", source, "sh", "-c", "test ! -e /usr/local/bin/tar; printf '#!/bin/sh\nexit 73\n' > /usr/local/bin/tar; chmod 755 /usr/local/bin/tar")
	rabbitFailedBackup(t, f, plan.ProjectID, "injected-failure")
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "exec", source, "rm", "/usr/local/bin/tar")
	ch = rabbitChannel(t, f.socket, source, "app")
	for _, kind := range []string{"classic", "quorum"} {
		if _, ok, e := ch.Get(kind, false); e != nil || !ok {
			t.Fatal("failed backup did not resume source", e)
		}
	}
	var backup lpclient.ServiceBackup
	if err = json.Unmarshal([]byte(f.cli.must("backups", "create", plan.ProjectID, "--request-id", "rabbit-backup", "--wait", "--json")), &backup); err != nil || backup.Status != "succeeded" {
		t.Fatalf("backup: %+v %v", backup, err)
	}
	ch = rabbitChannel(t, f.socket, source, "app")
	for _, kind := range []string{"classic", "quorum"} {
		count, e := ch.QueuePurge(kind, false)
		if e != nil || count != 2 {
			t.Fatalf("backup changed %s messages: %d %v", kind, count, e)
		}
	}
	if _, denied := observer.run("", "restores", "create", backup.ID, "--request-id", "denied"); !errors.Is(denied, lpclient.ErrForbidden) {
		t.Fatal("reader restore admission", denied)
	}
	var restored lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(f.cli.must("restores", "create", backup.ID, "--request-id", "rabbit-restore", "--wait", "--json")), &restored); err != nil || restored.Status != "succeeded" || restored.VerifiedAt == nil {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	t.Log("message backup restored and verified")
	copyID, err := id.Parse(restored.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	copyName := server.ContainerName(copyID)
	fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "restart", copyName)
	admin := rabbitChannel(t, f.socket, copyName, "admin")
	for _, kind := range []string{"classic", "quorum"} {
		got := map[string]int{}
		for range 2 {
			msg, ok, e := admin.Get(kind, true)
			if e != nil || !ok {
				t.Fatal("restored message unavailable", e)
			}
			got[string(msg.Body)]++
		}
		if got["pending"] != 1 || got["unacknowledged"] != 1 || len(got) != 2 {
			t.Fatalf("wrong restored %s payloads: %v", kind, got)
		}
		live, e := ch.QueueDeclarePassive(kind, true, false, false, false, amqp.Table{"x-queue-type": kind})
		if e != nil || live.Messages != 0 {
			t.Fatal("restore changed live source", e)
		}
	}
	for _, user := range []string{"admin", "app"} {
		if c, e := rabbitConnect(t, f.socket, copyName, user, rabbitPassword(t, f.socket, source, user)); e == nil {
			_ = c.Close()
			t.Fatal("copy accepted source credential")
		}
	}
	reader := rabbitChannel(t, f.socket, copyName, "app")
	if _, _, err = reader.Get("classic", true); err == nil {
		t.Fatal("recovery application can consume")
	}
	reader = rabbitChannel(t, f.socket, copyName, "app")
	if _, err = reader.QueueDeclare("forbidden", true, false, false, false, nil); err == nil {
		t.Fatal("recovery application can configure")
	}
	reader = rabbitChannel(t, f.socket, copyName, "app")
	closed := reader.NotifyClose(make(chan *amqp.Error, 1))
	if err = reader.PublishWithContext(t.Context(), "", "classic", false, false, amqp.Publishing{DeliveryMode: amqp.Persistent, Body: []byte("forbidden")}); err != nil {
		t.Fatal(err)
	}
	select {
	case denial := <-closed:
		if denial == nil || denial.Code != 403 {
			t.Fatal("unexpected publish denial", denial)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recovery application can publish")
	}
	var networkInfo struct {
		HostConfig      struct{ PortBindings map[string]json.RawMessage }
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
	}
	if err = json.Unmarshal([]byte(fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "inspect", copyName, "--format", "{{json .}}")), &networkInfo); err != nil || len(networkInfo.HostConfig.PortBindings) != 0 || len(networkInfo.NetworkSettings.Networks) != 1 {
		t.Fatal("recovery network exposure", err)
	}
	for _, network := range networkInfo.NetworkSettings.Networks {
		if fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "network", "inspect", network.NetworkID, "--format", "{{.Internal}}") != "true" {
			t.Fatal("recovery permits egress")
		}
	}
	f.cli.must("restores", "delete", restored.ID, "--yes")
	if volumes := fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+copyID.String()); volumes != "" {
		t.Fatal("copy volume survived cleanup")
	}
	holdCatalogBrowser(t, f.running.srv.URL, plan.ProjectID, f.connection)
	base := "/api/v1/projects/" + plan.ProjectID
	f.client.must(http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": "version: 1\nresources: {}\n"}, nil)
	var removal planBody
	f.client.must(http.StatusCreated, http.MethodPost, base+"/plans", map[string]any{"environment": "production", "catalog": true}, &removal)
	waitPlanned(t, f.client, &removal)
	if !removal.Executable {
		t.Fatal("removal not executable")
	}
	f.cli.must("approve", removal.ID, "--yes")
	if retained := fixtureCommand(t, "docker", "--host", "unix://"+f.socket, "volume", "ls", "-q", "--filter", "label=loomplane.instance="+iid.String()); retained == "" {
		t.Fatal("source storage was destroyed")
	}
	var after lpclient.ServiceRestore
	if err = json.Unmarshal([]byte(f.cli.must("restores", "create", backup.ID, "--request-id", "after-removal", "--wait", "--json")), &after); err != nil || after.Status != "succeeded" || after.VerifiedAt == nil {
		t.Fatalf("recovery after removal: %+v %v", after, err)
	}
	f.cli.must("restores", "delete", after.ID, "--yes")
	t.Log("RabbitMQ classic/quorum messages, acknowledgements, restart, backup outage, isolated recovery, fresh credentials, denied mutation and retained-source recovery passed")
}
