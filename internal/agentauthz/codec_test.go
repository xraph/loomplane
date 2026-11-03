package agentauthz_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/cortex"

	"github.com/xraph/loomplane/internal/agentauthz"
	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

// keyVerifier answers ValidatePrincipal with err and remembers who it
// was asked about.
type keyVerifier struct {
	mu    sync.Mutex
	err   error
	asked []security.Principal
}

func (v *keyVerifier) ValidatePrincipal(_ context.Context, p security.Principal) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked = append(v.asked, p)
	return v.err
}

// switchboard is each workspace's agent switch.
type switchboard struct {
	mu  sync.Mutex
	on  map[string]bool
	err error
}

func (s *switchboard) Enabled(_ context.Context, org string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on[org], s.err
}

func (s *switchboard) set(org string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on[org] = on
}

func newCodec(t *testing.T) (*agentauthz.Codec, *keyVerifier, *switchboard, *memberBook) {
	t.Helper()
	m := newMembers()
	authz := newAuthz(t, m)
	keys := &keyVerifier{}
	sw := &switchboard{on: map[string]bool{orgA: true, orgB: true}}
	return &agentauthz.Codec{Verifier: keys, Authz: authz, Enabled: sw.Enabled}, keys, sw, m
}

func roundTrip(t *testing.T, c *agentauthz.Codec, p security.Principal) (any, error) {
	t.Helper()
	data, err := c.EncodePrincipal(t.Context(), p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return c.DecodePrincipal(t.Context(), data)
}

func TestCodecRoundTripsBothIdentities(t *testing.T) {
	c, keys, _, _ := newCodec(t)
	keyed := security.Principal{UserID: ada, OrgID: orgA, KeyID: "key_1"}
	for name, p := range map[string]security.Principal{
		"alone":          as(orgA, ""),
		"for a person":   as(orgA, ada),
		"for an API key": security.AgentPrincipal(orgA, &keyed),
		"another org's":  as(orgB, bob),
	} {
		got, err := roundTrip(t, c, p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, p) {
			t.Fatalf("%s: got %#v, want %#v", name, got, p)
		}
	}
	if len(keys.asked) != 1 || keys.asked[0].KeyID != "key_1" {
		t.Fatalf("the key verifier was asked about %+v", keys.asked)
	}
	// A read-only person can't use the agent, so a run for one never
	// continues.
	if _, err := roundTrip(t, c, security.AgentPrincipal(orgA, &security.Principal{UserID: vi, OrgID: orgA, ReadOnly: true})); !errors.Is(err, cortex.ErrPrincipalRevoked) {
		t.Fatalf("for a read-only person: %v", err)
	}
}

func TestCodecRevokesWhenTheWorkspaceTurnsOff(t *testing.T) {
	c, _, sw, _ := newCodec(t)
	data, err := c.EncodePrincipal(t.Context(), as(orgA, ada))
	if err != nil {
		t.Fatal(err)
	}
	sw.set(orgA, false)
	_, err = c.DecodePrincipal(t.Context(), data)
	if !errors.Is(err, cortex.ErrPrincipalRevoked) || !strings.Contains(err.Error(), "the agent is off for this workspace") {
		t.Fatalf("agent off: %v", err)
	}
	// Not knowing is not the same as off: the run waits.
	sw.set(orgA, true)
	sw.mu.Lock()
	sw.err = errors.New("database unavailable")
	sw.mu.Unlock()
	_, err = c.DecodePrincipal(t.Context(), data)
	if !errors.Is(err, cortex.ErrPrincipalUnavailable) || errors.Is(err, cortex.ErrPrincipalRevoked) {
		t.Fatalf("settings unreadable: %v", err)
	}
}

func TestContinueAfterMemberRemovedFailsRevoked(t *testing.T) {
	c, _, _, members := newCodec(t)
	data, err := c.EncodePrincipal(t.Context(), as(orgA, ada))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DecodePrincipal(t.Context(), data); err != nil {
		t.Fatalf("while Ada is a member: %v", err)
	}
	members.remove(orgA, ada)
	must(t, c.Authz.Revoke(t.Context(), orgA, ada))
	_, err = c.DecodePrincipal(t.Context(), data)
	if !errors.Is(err, cortex.ErrPrincipalRevoked) {
		t.Fatalf("after Ada left: %v", err)
	}
	// The agent alone carries on: it belongs to the workspace, not to Ada.
	if _, err = roundTrip(t, c, as(orgA, "")); err != nil {
		t.Fatalf("the agent alone: %v", err)
	}
}

func TestCodecRevokesARevokedKey(t *testing.T) {
	c, keys, _, _ := newCodec(t)
	keyed := security.Principal{UserID: ada, OrgID: orgA, KeyID: "key_1"}
	data, err := c.EncodePrincipal(t.Context(), security.AgentPrincipal(orgA, &keyed))
	if err != nil {
		t.Fatal(err)
	}
	keys.err = apikeys.ErrRejected
	if _, err = c.DecodePrincipal(t.Context(), data); !errors.Is(err, cortex.ErrPrincipalRevoked) {
		t.Fatalf("a revoked key: %v", err)
	}
	keys.err = errors.New("saved API key authority is temporarily unavailable")
	if _, err = c.DecodePrincipal(t.Context(), data); !errors.Is(err, cortex.ErrPrincipalUnavailable) || errors.Is(err, cortex.ErrPrincipalRevoked) {
		t.Fatalf("a key store outage: %v", err)
	}
	// No verifier is no check, which fails closed.
	c.Verifier = nil
	if _, err = c.DecodePrincipal(t.Context(), data); err == nil {
		t.Fatal("decoded a keyed person without a verifier")
	}
}

func TestCodecRefusesNonAgentPrincipals(t *testing.T) {
	c, _, _, _ := newCodec(t)
	for name, p := range map[string]any{
		"a person":       security.Principal{UserID: ada, OrgID: orgA},
		"the automation": security.SystemPrincipal(orgA),
		"a pointer":      func() any { p := as(orgA, ada); return &p }(),
		"a string":       "agent:" + orgA,
		"nil":            nil,
		"a forged agent": security.Principal{Kind: security.KindAgent, UserID: security.AgentSubject(orgB), OrgID: orgA},
	} {
		if _, err := c.EncodePrincipal(t.Context(), p); err == nil {
			t.Errorf("encoded %s", name)
		}
	}
	// Stored bytes that aren't the agent are refused for good.
	for name, data := range map[string]string{
		"a person":    `{"UserID":"usr_ada","OrgID":"o"}`,
		"not JSON":    `agent`,
		"a forged ID": `{"UserID":"agent:o2","OrgID":"o","Kind":"agent"}`,
		"another org's person": `{"UserID":"agent:o","OrgID":"o","Kind":"agent",` +
			`"OnBehalfOf":{"UserID":"usr_bob","OrgID":"o2"}}`,
	} {
		if _, err := c.DecodePrincipal(t.Context(), []byte(data)); !errors.Is(err, cortex.ErrPrincipalRevoked) {
			t.Errorf("decoded %s: %v", name, err)
		}
	}
}

// A person who decides a checkpoint is encoded as a Decider, so cortex can
// record who decided. It's never a run's principal: those bytes decode as
// revoked, and nothing else a person could be is encoded.
func TestCodecEncodesThePersonWhoDecides(t *testing.T) {
	c, _, _, _ := newCodec(t)
	data, err := c.EncodePrincipal(t.Context(), agentauthz.Decider{Person: security.Principal{UserID: ada, OrgID: orgA}})
	if err != nil {
		t.Fatalf("encode a decider: %v", err)
	}
	if !strings.Contains(string(data), ada) || !strings.Contains(string(data), "decider") {
		t.Fatalf("decider bytes %s", data)
	}
	if _, err = c.DecodePrincipal(t.Context(), data); !errors.Is(err, cortex.ErrPrincipalRevoked) {
		t.Fatalf("a decider decoded as a run's principal: %v", err)
	}
	agent := as(orgA, ada)
	for name, d := range map[string]agentauthz.Decider{
		"nobody":              {},
		"no organization":     {Person: security.Principal{UserID: ada}},
		"the agent":           {Person: agent},
		"the automation":      {Person: security.SystemPrincipal(orgA)},
		"someone on behalf":   {Person: security.Principal{UserID: ada, OrgID: orgA, OnBehalfOf: &security.Principal{UserID: "usr_bob", OrgID: orgA}}},
		"an agent subject ID": {Person: security.Principal{UserID: security.AgentSubject(orgA), OrgID: orgA}},
	} {
		if _, encErr := c.EncodePrincipal(t.Context(), d); encErr == nil {
			t.Errorf("encoded a decider that is %s", name)
		}
	}
	if _, err = c.EncodePrincipal(t.Context(), &agentauthz.Decider{Person: security.Principal{UserID: ada, OrgID: orgA}}); err == nil {
		t.Error("encoded a pointer to a decider")
	}
}
