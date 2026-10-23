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
