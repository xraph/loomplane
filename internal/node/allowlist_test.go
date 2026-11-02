package node

import (
	"reflect"
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
