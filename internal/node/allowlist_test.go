package node

import (
	"context"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

const (
	eDocker = nodeproto.EndpointDocker
	eProbe  = nodeproto.EndpointProbe
	eKube   = nodeproto.EndpointKube
)

func TestAllows(t *testing.T) {
	c := Config{Endpoints: []nodeproto.Endpoint{eDocker}, IngressMode: "none"}
	if !c.Allows(nodeproto.EndpointControl) {
		t.Fatal("control must always be allowed")
	}
	if !c.Allows(eDocker) || c.Allows(eKube) || c.Allows(eProbe) {
		t.Fatal("only listed endpoints")
	}
	if c.Allows(nodeproto.EndpointPublic) {
		t.Fatal("public needs ingress")
	}
	for _, m := range []string{"relay", "direct"} {
		c.IngressMode = m
		if !c.Allows(nodeproto.EndpointPublic) {
			t.Fatalf("public with %s", m)
		}
	}
	c.IngressMode = ""
	if c.Allows(nodeproto.EndpointPublic) {
		t.Fatal("empty mode is none")
	}
	if c.Allows(nodeproto.Endpoint("bogus")) {
		t.Fatal("unknown endpoint")
	}
}

func TestNarrowNeverWidens(t *testing.T) {
	c := Config{Endpoints: []nodeproto.Endpoint{eDocker, eProbe}}
	got := c.Narrow([]nodeproto.Endpoint{eDocker, eProbe, eKube})
	want := []nodeproto.Endpoint{eDocker, eProbe}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(c.Endpoints, want) {
		t.Fatalf("got %v / %v", got, c.Endpoints)
	}
}

func TestNarrowStaleWiderAfterNarrower(t *testing.T) {
	c := Config{Endpoints: []nodeproto.Endpoint{eDocker, eProbe, eKube}}
	c.Narrow([]nodeproto.Endpoint{eDocker})
	got := c.Narrow([]nodeproto.Endpoint{eDocker, eProbe, eKube})
	if !reflect.DeepEqual(got, []nodeproto.Endpoint{eDocker}) || c.Allows(eKube) {
		t.Fatalf("stale narrow widened: %v", got)
	}
	if got := c.Narrow(nil); len(got) != 0 || c.Allows(eDocker) {
		t.Fatalf("empty keep: %v", got)
	}
}

func TestLiveLoadDoesNotAlias(t *testing.T) {
	l := NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker, eProbe}})
	got := l.Load()
	got.Endpoints[0] = eKube
	if l.Load().Allows(eKube) || !l.Load().Allows(eDocker) {
		t.Fatal("Load aliases the stored slice")
	}
	before := l.Load()
	l.Update(func(c *Config) { c.Narrow([]nodeproto.Endpoint{eDocker}) })
	if len(before.Endpoints) != 2 {
		t.Fatal("Update mutated an earlier snapshot")
	}
}

func TestLiveConcurrentNarrowAndServe(t *testing.T) {
	l := NewLive(Config{Endpoints: []nodeproto.Endpoint{eDocker, eProbe, eKube}, IngressMode: nodeproto.IngressNone})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			l.Update(func(c *Config) { c.Narrow([]nodeproto.Endpoint{eDocker, eProbe, eKube}) })
			l.Update(func(c *Config) {
				if i%2 == 0 {
					c.IngressMode = nodeproto.IngressRelay
				} else {
					c.IngressMode = nodeproto.IngressNone
				}
			})
		}
		l.Update(func(c *Config) { c.Narrow([]nodeproto.Endpoint{eDocker}) })
	}()
	go func() {
		defer wg.Done()
		r := &Registry{Log: discardLog()}
		for i := 0; i < 300; i++ {
			_ = l.Load().Allows(eKube)
			a, b := net.Pipe()
			go func() {
				_ = nodeproto.WriteHeader(a, nodeproto.StreamHeader{Endpoint: eKube})
				_, _ = io.Copy(io.Discard, a)
			}()
			r.Serve(context.Background(), l, b)
			_ = a.Close()
		}
	}()
	wg.Wait()
	if l.Load().Allows(eKube) {
		t.Fatal("narrowed endpoint still allowed")
	}
}

func TestNarrowKeepsPendingUntilAcknowledged(t *testing.T) {
	c := Config{Endpoints: []nodeproto.Endpoint{eProbe, eDocker}, PendingAllowed: []nodeproto.Endpoint{eDocker}}
	// A narrow made before Loomplane heard of docker cannot remove it.
	got := c.Narrow([]nodeproto.Endpoint{eProbe})
	if !reflect.DeepEqual(got, []nodeproto.Endpoint{eProbe, eDocker}) || !reflect.DeepEqual(c.PendingAllowed, []nodeproto.Endpoint{eDocker}) {
		t.Fatalf("pending was narrowed away: %v / %v", got, c.PendingAllowed)
	}
	// One that includes it is the acknowledgement.
	c.Narrow([]nodeproto.Endpoint{eProbe, eDocker})
	if len(c.PendingAllowed) != 0 || !reflect.DeepEqual(c.Endpoints, []nodeproto.Endpoint{eProbe, eDocker}) {
		t.Fatalf("not acknowledged: %v / %v", c.Endpoints, c.PendingAllowed)
	}
	// Now an admin narrow removes it like any other.
	c.Narrow([]nodeproto.Endpoint{eProbe})
	if !reflect.DeepEqual(c.Endpoints, []nodeproto.Endpoint{eProbe}) {
		t.Fatalf("an acknowledged endpoint survived a narrow: %v", c.Endpoints)
	}
}

func TestNarrowStillRemovesNonPending(t *testing.T) {
	c := Config{Endpoints: []nodeproto.Endpoint{eProbe, eDocker, eKube}, PendingAllowed: []nodeproto.Endpoint{eKube}}
	c.Narrow([]nodeproto.Endpoint{eProbe})
	if !reflect.DeepEqual(c.Endpoints, []nodeproto.Endpoint{eProbe, eKube}) {
		t.Fatalf("got %v", c.Endpoints)
	}
}
