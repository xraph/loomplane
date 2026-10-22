package deployments

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
)

type dialCredentials map[string]*connections.Connection

func (c dialCredentials) Credential(_ context.Context, orgID, id string) (*connections.Connection, []byte, error) {
	conn, ok := c[id]
	if !ok || conn.OrgID != orgID {
		return nil, nil, connections.ErrNotFound
	}
	return conn, []byte(conn.Details["key"]), nil
}

func sshKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(raw)
}

func TestTargetDialFromTheTargetsNetwork(t *testing.T) {
	host, _ := sshKey(t)
	_, user := sshKey(t)
	creds := dialCredentials{
		"conn_server": {ID: "conn_server", OrgID: "org", Cloud: connections.Server, Status: connections.StatusReady, Details: map[string]string{
			"key": user, "host": "server.example", "user": "deploy", "docker_socket": "/var/run/docker.sock",
			"host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))),
		}},
		"conn_cluster": {ID: "conn_cluster", OrgID: "org", Cloud: connections.Kubernetes, Status: connections.StatusReady, Details: map[string]string{}},
		"conn_broken":  {ID: "conn_broken", OrgID: "org", Cloud: connections.Server, Status: connections.StatusFailed, Details: map[string]string{}},
	}

	// A server dials over its pinned SSH connection, opened the way a
	// deployment opens it.
	var dialed *server.Config
	servers := &OwnedTarget{Cloud: connections.Server, Credentials: creds, ServerDial: func(_ context.Context, cfg *server.Config) (*server.Client, error) {
		dialed = cfg
		return nil, errors.New("ssh: handshake refused")
	}}
	if _, _, err := servers.TargetDial(t.Context(), "org", "conn_server"); err == nil || !strings.Contains(err.Error(), "handshake refused") {
		t.Fatalf("server dial: %v", err)
	}
	if dialed == nil || dialed.Address != "server.example:22" || string(dialed.HostKey.Marshal()) != string(host.PublicKey().Marshal()) {
		t.Fatalf("SSH config: %+v", dialed)
	}

	// A cluster's checks dial from Loomplane.
	upstream, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		if conn, acceptErr := upstream.Accept(); acceptErr == nil {
			_, _ = io.WriteString(conn, "ok")
			_ = conn.Close()
		}
	}()
	clusters := &OwnedTarget{Cloud: connections.Kubernetes, Credentials: creds}
	dial, closer, err := clusters.TargetDial(t.Context(), "org", "conn_cluster")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(t.Context(), "tcp", upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(conn)
	_ = conn.Close()
	if string(got) != "ok" || closer.Close() != nil {
		t.Fatalf("cluster dial read %q", got)
	}

	for name, tc := range map[string]struct {
		target    *OwnedTarget
		org, conn string
	}{
		"not ready":          {servers, "org", "conn_broken"},
		"another org":        {servers, "other", "conn_server"},
		"another cloud":      {servers, "org", "conn_cluster"},
		"unknown connection": {clusters, "org", "conn_gone"},
		"unowned provider":   {&OwnedTarget{Cloud: "gcp", Credentials: creds}, "org", "conn_server"},
		"no credentials":     {&OwnedTarget{Cloud: connections.Server}, "org", "conn_server"},
	} {
		if dial, closer, err := tc.target.TargetDial(t.Context(), tc.org, tc.conn); err == nil || dial != nil || closer != nil {
			t.Fatalf("%s: dialed (%v)", name, err)
		}
	}
}
