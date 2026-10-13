package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func sshFixture(t *testing.T) (*Config, ssh.Signer) {
	t.Helper()
	host, _ := testKey(t)
	user, raw := testKey(t)
	dir, err := os.MkdirTemp("/tmp", "lp-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	unix, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"Version":"29.8.2","APIVersion":"1.53","Os":"linux","Arch":"arm64"}`)
		case "/info":
			_, _ = io.WriteString(w, `{"ID":"isolated-daemon","OSType":"linux","Architecture":"aarch64"}`)
		default:
			w.WriteHeader(404)
		}
	})}
	go func() { _ = httpServer.Serve(unix) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sc := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(user.PublicKey().Marshal()) {
			return nil, errors.New("refused")
		}
		return nil, nil
	}}
	sc.AddHostKey(host)
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			go func() {
				ss, channels, requests, handshakeErr := ssh.NewServerConn(conn, sc)
				if handshakeErr != nil {
					_ = conn.Close()
					return
				}
				defer ss.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					var payload struct {
						Path, Reserved string
						Reserved2      uint32
					}
					if channel.ChannelType() != "direct-streamlocal@openssh.com" || ssh.Unmarshal(channel.ExtraData(), &payload) != nil || payload.Path != socket {
						_ = channel.Reject(ssh.Prohibited, "socket refused")
						continue
					}
					remote, dialErr := (&net.Dialer{}).DialContext(t.Context(), "unix", payload.Path)
					if dialErr != nil {
						_ = channel.Reject(ssh.ConnectionFailed, "socket unavailable")
						continue
					}
					local, requests, channelErr := channel.Accept()
					if channelErr != nil {
						_ = remote.Close()
						continue
					}
					go ssh.DiscardRequests(requests)
					go func() { _, _ = io.Copy(local, remote); _ = local.Close(); _ = remote.Close() }()
					go func() { _, _ = io.Copy(remote, local); _ = remote.Close(); _ = local.Close() }()
				}
			}()
		}
	}()
	hostname, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(raw, map[string]string{"host": hostname, "port": port, "user": "deploy", "host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey()))), "docker_socket": socket})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, host
}

func TestClient_readsDockerThroughPinnedSSH(t *testing.T) {
	cfg, _ := sshFixture(t)
	c, err := Dial(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	details, missing, err := c.Check(t.Context())
	if err != nil || len(missing) != 0 || details["docker_id"] != "isolated-daemon" || details["architecture"] != "aarch64" {
		t.Fatalf("check: %v %v %v", details, missing, err)
	}
}

func TestClient_rejectsUnexpectedHostKeyAndUserKey(t *testing.T) {
	for _, kind := range []string{"host", "user"} {
		t.Run(kind, func(t *testing.T) {
			cfg, _ := sshFixture(t)
			wrong, _ := testKey(t)
			if kind == "host" {
				cfg.HostKey = wrong.PublicKey()
			} else {
				cfg.Signer = wrong
			}
			if c, err := Dial(t.Context(), cfg); !errors.Is(err, ErrRejected) {
				if c != nil {
					_ = c.Close()
				}
				t.Fatalf("unexpected key accepted: %v", err)
			}
		})
	}
}

func TestClient_canceledDial(t *testing.T) {
	cfg, _ := sshFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if c, err := Dial(ctx, cfg); !errors.Is(err, context.Canceled) {
		if c != nil {
			_ = c.Close()
		}
		t.Fatalf("canceled dial: %v", err)
	}
}
