package node

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// relayTo serves one relay stream to target with allowed, dialing through a
// fake that sends every allowed address to an echo listener and records it.
func relayTo(t *testing.T, target string, allowed []string, dialErr error) (net.Conn, chan struct{}, *[]string) {
	t.Helper()
	echo := echoListener(t)
	var dialed []string
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if dialErr != nil {
			return nil, dialErr
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
	}
	r := &Registry{Log: discardLog()}
	r.Register(nodeproto.EndpointRelay, RelayHandler(func() []netip.Prefix { return ParseRelayAllow(allowed) }, dial))
	live := NewLive(Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointRelay}})
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointRelay, Target: target})
	return c, done, &dialed
}

func TestRelayReachesAnAllowedNetwork(t *testing.T) {
	c, done, dialed := relayTo(t, "10.9.0.5:22", []string{"10.9.0.0/16"}, nil)
	expectEcho(t, c)
	_ = c.Close()
	<-done
	if len(*dialed) != 1 || (*dialed)[0] != "10.9.0.5:22" {
		t.Fatalf("dialed %v, want the checked address", *dialed)
	}
}

func TestRelayRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		allowed      []string
		dialErr      error
		want         string
	}{
		{"outside the allowed networks", "10.10.0.5:22", []string{"10.9.0.0/16"}, nil, "this runner may not relay to 10.10.0.5:22"},
		{"nothing allowed", "10.9.0.5:22", nil, nil, "this runner may not relay to 10.9.0.5:22"},
		{"cloud metadata even when everything is allowed", "169.254.169.254:80", []string{"0.0.0.0/0"}, nil, "this runner may not relay to 169.254.169.254:80"},
		{"loopback even when everything is allowed", "127.0.0.1:22", []string{"0.0.0.0/0"}, nil, "this runner may not relay to 127.0.0.1:22"},
		{"a name is checked by the address it resolves to", "localhost:22", []string{"0.0.0.0/0", "::/0"}, nil, "this runner may not relay to localhost:22"},
		{"an IPv4-mapped metadata address", "[::ffff:169.254.169.254]:80", []string{"0.0.0.0/0", "::/0"}, nil, "this runner may not relay to [::ffff:169.254.169.254]:80"},
		{"an allowed address that doesn't answer", "10.9.0.5:22", []string{"10.9.0.0/16"}, errors.New("connection refused"), "this runner couldn't reach 10.9.0.5:22"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, done, _ := relayTo(t, tc.target, tc.allowed, tc.dialErr)
			v, line := readRefusal(t, c)
			if v != nodeproto.VerdictRefused || !strings.HasPrefix(line, tc.want) {
				t.Fatalf("verdict %d %q, want a refusal starting %q", v, line, tc.want)
			}
			<-done
		})
	}
}

func TestParseRelayAllowKeepsValidPrefixes(t *testing.T) {
	got := ParseRelayAllow([]string{"10.0.0.7/8", "not-a-cidr", "fd00::/8", ""})
	if len(got) != 2 || got[0].String() != "10.0.0.0/8" || got[1].String() != "fd00::/8" {
		t.Fatalf("got %v", got)
	}
}

// TestHTTPStreamHandlerServesAStreamAsOneConnection checks a runner's HTTP
// endpoint answers several requests on one stream and lets Serve finish once
// the caller closes it.
func TestHTTPStreamHandlerServesAStreamAsOneConnection(t *testing.T) {
	r := &Registry{Log: discardLog()}
	r.Register(nodeproto.EndpointFirecracker, HTTPStreamHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, "ok "+req.URL.Path)
	})))
	live := NewLive(Config{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointFirecracker}})
	c, done := serveWith(t, r, live, nodeproto.StreamHeader{Endpoint: nodeproto.EndpointFirecracker})
	br := bufio.NewReader(c)
	if v, err := br.ReadByte(); err != nil || v != nodeproto.VerdictAccepted {
		t.Fatalf("verdict %d %v", v, err)
	}
	for _, path := range []string{"/v1/capabilities", "/v1/status"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://runner/"+strings.TrimPrefix(path, "/"), http.NoBody)
		if err := req.Write(c); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != "ok "+path {
			t.Fatalf("%s answered %q", path, body)
		}
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not finish after the caller closed the stream")
	}
}
