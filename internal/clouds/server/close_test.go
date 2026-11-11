package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type closeFixtureAccess struct {
	HostAccess
	docker net.Conn
	close  func() error
}

func (a closeFixtureAccess) DialDocker(context.Context) (net.Conn, error) { return a.docker, nil }

func (a closeFixtureAccess) Close() error { return a.close() }

type closeFixtureIdleConn struct {
	net.Conn
	beforeClose func()
}

func (c closeFixtureIdleConn) Close() error {
	c.beforeClose()
	return c.Conn.Close()
}

func TestClientCloseShutsDownAccessBeforeIdleDocker(t *testing.T) {
	accessErr := errors.New("fixture access close error")
	for _, tc := range []struct {
		name       string
		without    bool
		wantErr    error
		wantEvents []string
	}{
		{name: "access success", wantEvents: []string{"access", "idle"}},
		{name: "access error", wantErr: accessErr, wantEvents: []string{"access", "idle"}},
		{name: "no access", without: true, wantEvents: []string{"idle"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, events, release := idleDockerCloseFixture(t, tc.wantErr)
			if tc.without {
				client.access = nil
				release()
			}
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				result <- client.Close()
			}()
			t.Cleanup(func() {
				release()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("client close did not finish after fixture release")
				}
			})
			for _, want := range tc.wantEvents {
				select {
				case event := <-events:
					if event != want {
						t.Fatalf("idle Docker connection tried to close before host access: got %s, want %s", event, want)
					}
				case <-time.After(time.Second):
					t.Fatal("client close did not reach the expected operation")
				}
			}
			select {
			case err := <-result:
				if !errors.Is(err, tc.wantErr) {
					t.Fatal("client did not preserve the access close result")
				}
			case <-time.After(time.Second):
				t.Fatal("client close did not return after idle Docker cleanup")
			}
		})
	}
}

func idleDockerCloseFixture(t *testing.T, accessErr error) (*Client, <-chan string, func()) {
	t.Helper()
	local, peer := net.Pipe()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(func() {
		release()
		_ = local.Close()
		_ = peer.Close()
	})
	events := make(chan string, 2)
	var pooled atomic.Bool
	access := closeFixtureAccess{
		docker: closeFixtureIdleConn{Conn: local, beforeClose: func() {
			if !pooled.Load() {
				return
			}
			events <- "idle"
			<-gate
		}},
		close: func() error {
			events <- "access"
			release()
			return accessErr
		},
	}
	client := newClient(&Config{}, access)
	served := make(chan error, 1)
	go func() {
		request, err := http.ReadRequest(bufio.NewReader(peer))
		if err == nil {
			err = request.Body.Close()
		}
		if err == nil {
			_, err = io.WriteString(peer, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		}
		served <- err
	}()
	pool := make(chan error, 1)
	trace := &httptrace.ClientTrace{PutIdleConn: func(err error) { pool <- err }}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)
	ctx = httptrace.WithClientTrace(ctx, trace)
	response, err := client.Request(ctx, http.MethodGet, "/fixture", nil)
	if err != nil {
		t.Fatal("fixture HTTP request failed")
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
		t.Fatal("fixture HTTP response failed")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal("fixture did not serve the Docker response")
		}
	case <-ctx.Done():
		t.Fatal("fixture response did not finish")
	}
	select {
	case err := <-pool:
		if err != nil {
			t.Fatal("fixture Docker connection was not pooled")
		}
	case <-ctx.Done():
		t.Fatal("fixture did not observe the idle Docker connection")
	}
	pooled.Store(true)
	return client, events, release
}

func TestClientCloseWithoutTransport(t *testing.T) {
	if err := (&Client{}).Close(); err != nil {
		t.Fatal("empty client did not close cleanly")
	}
	want := errors.New("fixture access close error")
	calls := 0
	client := &Client{access: closeFixtureAccess{close: func() error {
		calls++
		return want
	}}}
	if err := client.Close(); !errors.Is(err, want) || calls != 1 {
		t.Fatal("client did not close access exactly once without a transport")
	}
}
