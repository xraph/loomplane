package approvaladmission

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

func admissionFixture(t *testing.T) (Claims, Expected, []byte) {
	t.Helper()
	id := func(prefix string) string { return typeid.MustGenerate(prefix).String() }
	at := time.Date(2026, 11, 4, 12, 0, 0, 123456789, time.UTC)
	c := Claims{Version: 1, Operation: Operation, AppID: id("aapp"), Scope: Scope{OrgID: id("aorg"), ProjectID: id("proj"), EnvironmentID: id("env"), PlanID: id("plan"), Revision: strings.Repeat("a", 64)}, RequestID: id("dec"), GroupSetDigest: strings.Repeat("b", 64), Note: "reviewed", Source: SessionSource, UserID: id("ausr"), SessionID: id("ases"), AdmittedAt: at, AcceptBefore: at.Add(time.Hour)}
	return c, Expected{AppID: c.AppID, Request: Request{Scope: c.Scope, RequestID: c.RequestID, GroupSetDigest: c.GroupSetDigest, Note: c.Note, Actor: security.Principal{UserID: c.UserID, OrgID: c.OrgID}, AcceptBefore: c.AcceptBefore}}, []byte(strings.Repeat("k", 32))
}
func testEnvelope(key, payload []byte) Envelope {
	prefix := "v1." + KeyID(key) + "." + base64.RawURLEncoding.EncodeToString(payload)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(KeyPurpose + "\x00" + prefix))
	return Envelope(prefix + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)))
}
func TestApprovalAdmissionStrictCodec(t *testing.T) {
	c, want, key := admissionFixture(t)
	raw, err := Payload(c)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	envelope := testEnvelope(key, raw)
	got, err := v.Verify(envelope, want)
	if err != nil {
		t.Fatal(err)
	}
	copyClaims := got.Claims()
	copyClaims.UserID = "changed"
	if got.Actor().UserID != c.UserID || got.Claims().UserID == copyClaims.UserID {
		t.Fatal("verified claims are mutable")
	}
	cases := map[string]string{
		"duplicate":  strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		"unknown":    strings.Replace(string(raw), `"version":1`, `"unexpected":1,"version":1`, 1),
		"case alias": strings.Replace(string(raw), `"version"`, `"Version"`, 1),
		"null":       strings.Replace(string(raw), `"key_id":""`, `"key_id":null`, 1),
		"missing":    strings.Replace(string(raw), `"key_id":"",`, "", 1),
		"trailing":   string(raw) + `{}`,
		"whitespace": string(raw) + " ",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(testEnvelope(key, []byte(raw)), want); err == nil {
				t.Fatal("accepted malformed authenticated claims")
			}
		})
	}
	for _, bad := range []Envelope{envelope + ".extra", envelope + "\n", Envelope("v2" + string(envelope)[2:]), Envelope(strings.Repeat("x", MaxEnvelopeBytes+1)), envelope[:len(envelope)-2] + "xx"} {
		if _, err := v.Verify(bad, want); err == nil {
			t.Fatal("accepted malformed envelope")
		}
	}
	if _, err := new(Verifier).Verify(envelope, want); err == nil {
		t.Fatal("zero verifier accepted provenance")
	}
	rotated, _ := NewVerifier([]byte(strings.Repeat("z", 32)))
	if _, err := rotated.Verify(envelope, want); err == nil {
		t.Fatal("rotation accepted old provenance")
	}
}
func TestApprovalAdmissionClaimsAndExactBinding(t *testing.T) {
	c, want, key := admissionFixture(t)
	v, _ := NewVerifier(key)
	raw, _ := Payload(c)
	envelope := testEnvelope(key, raw)
	mutations := map[string]func(*Claims){
		"mixed":             func(c *Claims) { c.KeyID = typeid.MustGenerate("akey").String() },
		"unknown source":    func(c *Claims) { c.Source = "oauth" },
		"time equality":     func(c *Claims) { c.AdmittedAt = c.AcceptBefore },
		"time reverse":      func(c *Claims) { c.AdmittedAt = c.AcceptBefore.Add(time.Nanosecond) },
		"bad ID":            func(c *Claims) { c.UserID = "user" },
		"upper digest":      func(c *Claims) { c.Revision = strings.Repeat("A", 64) },
		"noncanonical note": func(c *Claims) { c.Note = " reviewed " },
		"note too long":     func(c *Claims) { c.Note = strings.Repeat("x", 2001) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := c
			mutate(&bad)
			b, _ := json.Marshal(bad)
			if _, err := v.Verify(testEnvelope(key, b), want); err == nil {
				t.Fatal("accepted invalid claims")
			}
		})
	}
	bindings := map[string]func(*Expected){
		"app":          func(e *Expected) { e.AppID = typeid.MustGenerate("aapp").String() },
		"request":      func(e *Expected) { e.RequestID = typeid.MustGenerate("dec").String() },
		"actor":        func(e *Expected) { e.Actor.UserID = typeid.MustGenerate("ausr").String() },
		"key fallback": func(e *Expected) { e.Actor.KeyID = typeid.MustGenerate("akey").String() },
		"organization": func(e *Expected) { e.OrgID = typeid.MustGenerate("aorg").String() },
		"project":      func(e *Expected) { e.ProjectID = typeid.MustGenerate("proj").String() },
		"environment":  func(e *Expected) { e.EnvironmentID = typeid.MustGenerate("env").String() },
		"plan":         func(e *Expected) { e.PlanID = typeid.MustGenerate("plan").String() },
		"revision":     func(e *Expected) { e.Revision = strings.Repeat("c", 64) },
		"groups":       func(e *Expected) { e.GroupSetDigest = strings.Repeat("c", 64) },
		"note":         func(e *Expected) { e.Note += "changed" },
		"deadline":     func(e *Expected) { e.AcceptBefore = e.AcceptBefore.Add(time.Nanosecond) },
		"readonly":     func(e *Expected) { e.Actor.ReadOnly = true },
	}
	for name, mutate := range bindings {
		t.Run(name, func(t *testing.T) {
			bad := want
			mutate(&bad)
			if _, err := v.Verify(envelope, bad); err == nil {
				t.Fatal("accepted substituted binding")
			}
		})
	}
	c.Source = KeySource
	c.SessionID = ""
	c.KeyID = typeid.MustGenerate("akey").String()
	want.Actor.KeyID = c.KeyID
	raw, err := Payload(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.Verify(testEnvelope(key, raw), want); err != nil {
		t.Fatal(err)
	}
}
func TestApprovalAdmissionMaximumNoteAndWholeEncoding(t *testing.T) {
	c, want, key := admissionFixture(t)
	c.Note = strings.Repeat("\x00", MaxNoteBytes)
	want.Note = c.Note
	raw, err := Payload(c)
	if err != nil {
		t.Fatal(err)
	}
	envelope := testEnvelope(key, raw)
	if len(envelope) <= 16*1024 || len(envelope) > MaxEnvelopeBytes {
		t.Fatalf("whole envelope length %d", len(envelope))
	}
	v, _ := NewVerifier(key)
	if _, err = v.Verify(envelope, want); err != nil {
		t.Fatal(err)
	}
	t.Logf("worst-case 2000-byte note: payload=%d envelope=%d cap=%d", len(raw), len(envelope), MaxEnvelopeBytes)
}
func TestApprovalAdmissionCompleteGroupDigest(t *testing.T) {
	c, _, _ := admissionFixture(t)
	g := []GroupPin{{ID: typeid.MustGenerate("pgroup").String(), Generation: 1, Revision: strings.Repeat("b", 64), ConfigRevision: strings.Repeat("c", 64)}, {ID: typeid.MustGenerate("pgroup").String(), Generation: 2, Revision: strings.Repeat("d", 64), ConfigRevision: strings.Repeat("e", 64)}}
	first, err := GroupSetDigest(c.Scope, g)
	if err != nil {
		t.Fatal(err)
	}
	g[0], g[1] = g[1], g[0]
	second, err := GroupSetDigest(c.Scope, g)
	if err != nil || first != second {
		t.Fatal("order changed digest")
	}
	g[0].Generation++
	third, _ := GroupSetDigest(c.Scope, g)
	if third == first {
		t.Fatal("generation not bound")
	}
	if _, err := GroupSetDigest(c.Scope, []GroupPin{g[0], g[0]}); err == nil {
		t.Fatal("duplicate groups accepted")
	}
	if _, err := GroupSetDigest(c.Scope, nil); err == nil {
		t.Fatal("empty set accepted")
	}
}
