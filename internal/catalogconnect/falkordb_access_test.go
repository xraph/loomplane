package catalogconnect

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFalkorEngineAccessFieldsAndEnvelopeBoundaries(t *testing.T) {
	a := falkorAccess()
	a.Host = "2001:db8::2"
	_, ca := searchTestCA(t)
	a.TLS, a.CACert = TLSVerifyFull, ca
	raw, err := EncodeEngineAccess(a)
	if err != nil {
		t.Fatal("valid FalkorDB envelope rejected")
	}
	parsed, err := ParseEngineAccess(raw)
	if err != nil || parsed.Port != 6379 || parsed.Username != a.Username {
		t.Fatal("FalkorDB port or username changed")
	}
	want := map[string]string{
		"url": "[2001:db8::2]:6379", "hostport": "[2001:db8::2]:6379", "host": "2001:db8::2", "port": "6379",
		"username": a.Username, "password": a.Password, "ca_cert": ca, "tls_mode": TLSVerifyFull, "database": "", "query": "",
	}
	a.CACert = ""
	for _, mode := range []string{TLSVerifyFull, TLSDisable} {
		a.TLS = mode
		withoutCA := falkorRaw(t, a)
		gotMode, modeErr := EngineAccessField(withoutCA, "tls_mode")
		gotCA, caErr := EngineAccessField(withoutCA, "ca_cert")
		if modeErr != nil || gotMode != mode || caErr != nil || gotCA != "" {
			t.Fatalf("explicit %s mode changed without an inline CA", mode)
		}
	}
	for field, expected := range want {
		if got, err := EngineAccessField(raw, field); err != nil || got != expected {
			t.Fatalf("normalized %s was incorrect", field)
		}
	}
	for _, field := range []string{"output:password", "secret:password", "admin-url", "graph"} {
		if got, err := EngineAccessField(raw, field); !errors.Is(err, ErrInvalid) || got != "" {
			t.Fatal("private or unsupported FalkorDB field resolved")
		}
	}
	for _, extra := range []string{`,"graph":"hidden"`, `,"database":"0"`, `,"query":"RETURN 1"`, `,"host":"other"`, `,"Host":"other"`} {
		if _, err := ParseEngineAccess(strings.TrimSuffix(raw, "}") + extra + "}"); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous envelope accepted")
		}
	}
	for _, invalid := range []EngineAccess{
		{Engine: a.Engine, Host: a.Host, Username: a.Username, Password: a.Password, TLS: TLSRequire},
		{Engine: a.Engine, Host: a.Host, Username: a.Username, Password: a.Password, TLS: TLSDisable, CACert: ca},
		{Engine: a.Engine, Host: a.Host, Username: a.Username, TLS: TLSDisable},
		{Engine: a.Engine, Host: a.Host, Username: a.Username, Password: a.Password + "\r\n", TLS: TLSDisable},
		{Engine: EngineElasticsearch, Host: a.Host, Username: a.Username, Password: a.Password, TLS: TLSDisable},
	} {
		if _, err := EncodeEngineAccess(invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid access accepted")
		}
	}
	for _, msg := range []string{fmt.Sprintf("%+v %#v", parsed, parsed), RedactEngineAccess(a.Password+" "+ca, raw)} {
		if strings.Contains(msg, a.Password) || strings.Contains(msg, "BEGIN CERTIFICATE") {
			t.Fatal("FalkorDB access leaked into a diagnostic")
		}
	}
}

func TestFalkorResolveRejectsStoredRowMismatch(t *testing.T) {
	a := falkorAccess()
	store := newMemStore()
	vault := memSecrets{}
	svc := NewService(store, vault, roleAuthz{})
	raw := falkorRaw(t, a)
	// Resolve reads the Vault slot and checks its identity against the row.
	for _, change := range []func(*Endpoint){
		func(e *Endpoint) { e.Engine = EngineRedis }, func(e *Endpoint) { e.Host = "other.example" },
		func(e *Endpoint) { e.Port++ }, func(e *Endpoint) { e.Username = "other" },
		func(e *Endpoint) { e.Database = "0" }, func(e *Endpoint) { e.TLS = TLSVerifyFull },
	} {
		e := Endpoint{ID: "falkor", OrgID: "org", Engine: a.Engine, Host: a.Host, Port: 6379, Username: a.Username, TLS: a.TLS}
		change(&e)
		store.rows[e.ID] = e
		vault[DSNKey(e.OrgID, e.ID)] = []byte(raw)
		if _, got, err := svc.Resolve(t.Context(), e.OrgID, e.ID); err == nil || got != "" {
			t.Fatal("stored row mismatch resolved")
		}
	}
	e := Endpoint{Engine: a.Engine}
	other := a
	other.Engine = EngineElasticsearch
	other.Username = "search"
	encoded, err := json.Marshal(other)
	if err != nil {
		t.Fatal("marshal test access")
	}
	if err := parseDSN(&e, string(encoded)); !errors.Is(err, ErrInvalid) {
		t.Fatal("search envelope accepted on a FalkorDB row")
	}
}
