package catalogconnect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func searchAccess(engine string) EngineAccess {
	mode := TLSVerifyFull
	if engine == EngineElasticsearch {
		mode = TLSDisable
	}
	return EngineAccess{Engine: engine, Host: "search.example", Username: "app", Password: "s3cret:short/x?", TLS: mode}
}

func TestEngineAccessStrictRoundTripAndFields(t *testing.T) {
	for _, engine := range []string{EngineElasticsearch, EngineOpenSearch} {
		a := searchAccess(engine)
		raw, err := EncodeEngineAccess(a)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseEngineAccess(raw)
		if err != nil || parsed.Port != 9200 || parsed.Password != a.Password {
			t.Fatal("access round trip failed")
		}
		for field, want := range map[string]string{"host": a.Host, "hostport": "search.example:9200", "port": "9200", "username": "app", "password": a.Password, "database": "", "query": "", "ca_cert": ""} {
			got, fieldErr := EngineAccessField(raw, field)
			if fieldErr != nil || got != want {
				t.Errorf("field %s did not match", field)
			}
		}
		address, err := EngineAccessField(raw, "url")
		wantURL := "https://search.example:9200"
		if engine == EngineElasticsearch {
			wantURL = "http://search.example:9200"
		}
		u, parseErr := url.Parse(address)
		if err != nil || parseErr != nil || address != wantURL || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			t.Fatal("normalized URL failed")
		}
		for _, field := range []string{"secret:password", "output:url", "admin-url", "ca_file"} {
			if _, err = EngineAccessField(raw, field); !errors.Is(err, ErrInvalid) {
				t.Errorf("accepted field %s", field)
			}
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if strings.Contains(fmt.Sprintf(format, a), a.Password) {
				t.Fatal("formatted credentials leaked")
			}
		}
	}
}

func TestEngineAccessRejectsAmbiguousOrUnqualifiedInput(t *testing.T) {
	raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
	invalid := []string{
		raw + "{}", strings.Replace(raw, `"host":`, `"Host":`, 1), strings.Replace(raw, `"host":`, `"host":"other","host":`, 1),
		strings.Replace(raw, `"port":9200`, `"port":null`, 1), strings.Replace(raw, `"port":9200`, `"port":1.5`, 1),
		strings.Replace(raw, `"engine":"elasticsearch"`, `"engine":"duckdb"`, 1),
		strings.Replace(raw, `"host":"search.example"`, `"host":"search.example/other"`, 1), strings.Replace(raw, `"host":"search.example"`, `"host":"search.example:9200"`, 1),
		strings.Replace(raw, `"tls":"disable"`, `"tls":"require"`, 1), strings.Replace(raw, `"tls":"disable"`, `"tls":"insecure"`, 1),
		strings.TrimSuffix(raw, "}") + `,"ca_cert":"/etc/ssl/cert.pem"}`,
		strings.TrimSuffix(raw, "}") + `,"database":"remote"}`,
		strings.TrimSuffix(raw, "}") + `,"endpoint":"https://another.example"}`,
		strings.Repeat(" ", maxEngineAccess+1), "null", "[]",
	}
	for i, input := range invalid {
		if _, err := ParseEngineAccess(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid case %d", i)
		}
	}
	for _, host := range []string{"", "host,path", "user@host", "host?x", "host#x", "[::1]", "fe80::1%eth0", "a..b", "-a.example", "host ", "/socket", strings.Repeat("a", 254)} {
		a := searchAccess(EngineElasticsearch)
		a.Host = host
		if _, err := EncodeEngineAccess(a); !errors.Is(err, ErrInvalid) {
			t.Error("accepted invalid host")
		}
	}
	a := searchAccess(EngineOpenSearch)
	a.TLS = TLSDisable
	if _, err := EncodeEngineAccess(a); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted plaintext OpenSearch")
	}
}

func TestEngineAccessVaultRowsAuditsAndMismatch(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
	var events []AuditEvent
	svc.Audit = searchAuditFunc(func(_ context.Context, event AuditEvent) { events = append(events, event) })
	e, err := svc.Create(t.Context(), admin, CreateInput{Name: "external-search", Engine: EngineElasticsearch, DSN: raw})
	if err != nil {
		t.Fatal(err)
	}
	if e.Host != "search.example" || e.Port != 9200 || e.TLS != TLSDisable || e.Database != "" || string(secrets[DSNKey("org", e.ID)]) != raw {
		t.Fatal("stored access or row mismatch")
	}
	row, _ := json.Marshal(e)
	audit, _ := json.Marshal(events)
	if strings.Contains(string(row), "s3cret") || strings.Contains(string(audit), "s3cret") || len(events) != 1 || events[0].Address != "search.example:9200" {
		t.Fatal("row or audit leaked credentials")
	}
	if _, _, err = svc.Resolve(t.Context(), "org", e.ID); err != nil {
		t.Fatal(err)
	}
	original := store.rows[e.ID]
	for _, mutate := range []func(*Endpoint){func(e *Endpoint) { e.Host = "other.example" }, func(e *Endpoint) { e.Port++ }, func(e *Endpoint) { e.Username = "other" }, func(e *Endpoint) { e.Database = "other" }, func(e *Endpoint) { e.TLS = TLSVerifyFull }} {
		changed := original
		mutate(&changed)
		store.rows[e.ID] = changed
		if _, _, err = svc.Resolve(t.Context(), "org", e.ID); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted row-envelope mismatch")
		}
	}
	store.rows[e.ID] = original
	if _, err = svc.Create(t.Context(), viewer, CreateInput{Name: "denied", Engine: EngineElasticsearch, DSN: raw}); err == nil {
		t.Fatal("viewer created access")
	}
}

func TestEngineAccessRedactsShortAndEncodedPasswords(t *testing.T) {
	for _, password := range []string{"x", "a?b/x", "quoted\"secret"} {
		a := searchAccess(EngineElasticsearch)
		a.Password = password
		raw, _ := EncodeEngineAccess(a)
		escaped, _ := json.Marshal(password)
		for _, echo := range []string{password, url.QueryEscape(password), url.PathEscape(password), string(escaped[1 : len(escaped)-1]), base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + password))} {
			if got := redact(echo, raw); strings.Contains(got, password) || got == echo {
				t.Fatal("secret echo was not redacted")
			}
		}
	}
}

func FuzzEngineAccess(f *testing.F) {
	raw, _ := EncodeEngineAccess(searchAccess(EngineElasticsearch))
	f.Add(raw)
	f.Add(`{"engine":"opensearch"}`)
	f.Add("null")
	f.Fuzz(func(t *testing.T, raw string) {
		a, err := ParseEngineAccess(raw)
		if err != nil {
			return
		}
		next, err := EncodeEngineAccess(a)
		if err != nil {
			t.Fatal("parsed access cannot encode")
		}
		if _, err = ParseEngineAccess(next); err != nil {
			t.Fatal("encoded access cannot parse")
		}
	})
}

type searchAuditFunc func(context.Context, AuditEvent)

func (f searchAuditFunc) Record(ctx context.Context, event AuditEvent) { f(ctx, event) }

func TestEngineAccessAuthorityOnlyURLs(t *testing.T) {
	for _, tc := range []struct {
		engine, tls, host, want string
		port                    int
	}{
		{EngineElasticsearch, TLSVerifyFull, "search.example", "https://search.example:9200", 0},
		{EngineElasticsearch, TLSDisable, "2001:db8::5", "http://[2001:db8::5]:9250", 9250},
		{EngineOpenSearch, TLSVerifyFull, "::1", "https://[::1]:9443", 9443},
	} {
		a := searchAccess(tc.engine)
		a.Host, a.Port, a.TLS = tc.host, tc.port, tc.tls
		a.Username, a.Password = "app/@?#", "p@ss:/?#%"
		raw, err := EncodeEngineAccess(a)
		if err != nil {
			t.Fatal(err)
		}
		got, err := EngineAccessField(raw, "url")
		if err != nil || got != tc.want {
			t.Fatal("authority URL mismatch")
		}
		u, err := url.Parse(got)
		if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			t.Fatal("URL contains access material")
		}
		for field, want := range map[string]string{"username": a.Username, "password": a.Password} {
			value, err := EngineAccessField(raw, field)
			if err != nil || value != want {
				t.Fatal("separate credential mismatch")
			}
		}
	}
}

func TestEngineAccessRejectsNullTypesAndCredentialBounds(t *testing.T) {
	a := searchAccess(EngineOpenSearch)
	raw, _ := EncodeEngineAccess(a)
	for _, field := range []string{"engine", "host", "port", "username", "password", "tls", "ca_cert"} {
		for _, invalid := range []string{"null", " null ", "{}", "[]", "true"} {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal([]byte(raw), &fields)
			fields[field] = json.RawMessage(invalid)
			data, _ := json.Marshal(fields)
			if _, err := ParseEngineAccess(string(data)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid %s", field)
			}
		}
	}
	for _, mutate := range []func(*EngineAccess){
		func(a *EngineAccess) { a.Port = -1 }, func(a *EngineAccess) { a.Port = 65536 }, func(a *EngineAccess) { a.Username = "x:y" },
		func(a *EngineAccess) { a.Username = strings.Repeat("u", 257) }, func(a *EngineAccess) { a.Password = strings.Repeat("p", 4097) },
		func(a *EngineAccess) { a.Password = "p\n" }, func(a *EngineAccess) { a.Password = string([]byte{0xff}) },
	} {
		b := a
		mutate(&b)
		if _, err := EncodeEngineAccess(b); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted invalid access bounds")
		}
	}
}

func TestEngineAccessResolveRejectsEveryRowIdentityMismatch(t *testing.T) {
	for _, engine := range []string{EngineElasticsearch, EngineOpenSearch} {
		store, secrets := newMemStore(), memSecrets{}
		svc := NewService(store, secrets, roleAuthz{})
		a := searchAccess(engine)
		a.TLS, a.CACert = TLSVerifyFull, ""
		_, a.CACert = searchTestCA(t)
		raw, err := EncodeEngineAccess(a)
		if err != nil {
			t.Fatal(err)
		}
		e, err := svc.Create(t.Context(), admin, CreateInput{Name: "search", Engine: engine, DSN: raw})
		if err != nil {
			t.Fatal(err)
		}
		original := store.rows[e.ID]
		for _, mutate := range []func(*Endpoint){func(e *Endpoint) { e.Engine = EnginePostgres }, func(e *Endpoint) {
			e.Engine = EngineOpenSearch
			if engine == EngineOpenSearch {
				e.Engine = EngineElasticsearch
			}
		}, func(e *Endpoint) { e.Host = "other.example" }, func(e *Endpoint) { e.Port++ }, func(e *Endpoint) { e.Username = "other" }, func(e *Endpoint) { e.Database = "other" }, func(e *Endpoint) { e.TLS = TLSDisable }} {
			row := original
			mutate(&row)
			store.rows[e.ID] = row
			endpoint, value, err := svc.Resolve(t.Context(), "org", e.ID)
			if !errors.Is(err, ErrInvalid) || endpoint != nil || value != "" {
				t.Fatal("row-envelope mismatch returned access")
			}
		}
		store.rows[e.ID] = original
		secrets[DSNKey("org", e.ID)] = []byte(strings.TrimSuffix(raw, "}") + `,"host":"other"}`)
		if _, value, err := svc.Resolve(t.Context(), "org", e.ID); !errors.Is(err, ErrInvalid) || value != "" {
			t.Fatal("malformed stored envelope resolved")
		}
		secrets[DSNKey("org", e.ID)] = []byte(raw)
		if endpoint, value, err := svc.Resolve(t.Context(), "org", e.ID); err != nil || endpoint.ID != e.ID || value != raw {
			t.Fatal("valid stored envelope failed")
		}
		row, _ := json.Marshal(e)
		if strings.Contains(string(row), a.CACert) || strings.Contains(string(row), a.Password) || strings.Contains(string(row), "ca_cert") {
			t.Fatal("endpoint row contains access material")
		}
	}
}
