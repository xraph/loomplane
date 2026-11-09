package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	cpprovider "github.com/xraph/ctrlplane/provider"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

func TestMemcachedReadinessChecksNativeReplyAndBoundsSilence(t *testing.T) {
	for _, reply := range []string{"VERSION 1.6.45\r\n", "ERROR\r\n", "+PONG\r\n", "VERSION \r\n", ""} {
		t.Run(reply, func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			go func() {
				line, err := bufio.NewReader(server).ReadString('\n')
				if err != nil || line != "version\r\n" {
					return
				}
				if reply != "" {
					_, _ = io.WriteString(server, reply)
				}
			}()
			err := pingMemcached(t.Context(), 50*time.Millisecond, func(context.Context) (net.Conn, error) { return client, nil })
			if (err == nil) != (reply == "VERSION 1.6.45\r\n") {
				t.Fatalf("invalid readiness: %q: %v", reply, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := pingMemcached(ctx, time.Second, func(ctx context.Context) (net.Conn, error) { return nil, ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestMemcachedEndpointKeepsItsNativeProtocol(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	r := &Runtime{}
	spec := cpprovider.ServiceSpec{Name: "cache", Ports: []cpprovider.PortSpec{{Container: 11211}, {Container: 9090}}}
	labels := map[string]string{owned.LabelHealthProtocol: "memcached", owned.LabelHealthPort: "11211"}
	endpoints := r.endpoints(&Client{}, iid, spec, labels)
	if len(endpoints) != 2 || endpoints[0].URL != "memcached://"+ContainerName(iid)+":11211" || endpoints[0].Protocol != "memcached" || endpoints[0].Public || endpoints[1].Protocol != "http" {
		t.Fatalf("native cache became an HTTP endpoint: %+v", endpoints)
	}
}

// A public port on a server with no advertise_host, as on a node whose
// address isn't confirmed yet, has no public URL rather than one with no
// host.
func TestPublicEndpointWithoutAnAddressHasNoURL(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	r := &Runtime{}
	spec := cpprovider.ServiceSpec{Name: "web", Ports: []cpprovider.PortSpec{{Container: 8080}}}
	labels := map[string]string{owned.LabelPublic: "true", owned.LabelHostPort: "30080"}
	for advertise, want := range map[string]string{"": "", "203.0.113.7": "http://203.0.113.7:30080"} {
		c := &Client{Config: &Config{Details: map[string]string{"advertise_host": advertise}}}
		endpoints := r.endpoints(c, iid, spec, labels)
		if len(endpoints) != 1 || endpoints[0].URL != want || !endpoints[0].Public || endpoints[0].Port != 30080 {
			t.Fatalf("advertise_host %q: %+v", advertise, endpoints)
		}
	}
}
