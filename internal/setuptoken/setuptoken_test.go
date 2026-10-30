package setuptoken

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

func key(b byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

var tokenShape = regexp.MustCompile(`^[a-z2-7]{32}$`)

func TestDeriveIsStable(t *testing.T) {
	a, err := Derive(key(1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Derive(key(1))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("not stable: %q vs %q", a, b)
	}
	if !tokenShape.MatchString(a) {
		t.Fatalf("bad shape: %q", a)
	}
}

func TestDeriveKnownAnswer(t *testing.T) {
	const k = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	const want = "737wzfewgo5pdfdpsvvw72fxlw6pdnnl"
	got, err := Derive(k)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Derive = %q, want %q", got, want)
	}
}

func TestDeriveDiffersByKey(t *testing.T) {
	a, _ := Derive(key(1))
	b, _ := Derive(key(2))
	if a == b {
		t.Fatalf("different keys gave the same token %q", a)
	}
}

func TestDeriveRejectsBadKey(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	for _, k := range []string{"short", "", short} {
		if _, err := Derive(k); err == nil {
			t.Errorf("Derive(%q) expected error", k)
		}
	}
}

func TestMatchesForgivesCopyPaste(t *testing.T) {
	k := key(3)
	tok, err := Derive(k)
	if err != nil {
		t.Fatal(err)
	}
	if !Matches(k, " "+strings.ToUpper(tok)+"\n") {
		t.Fatal("expected match")
	}
}

func TestMatchesRefuses(t *testing.T) {
	k := key(3)
	tok, _ := Derive(k)
	near := "b" + tok[1:]
	if tok[0] == 'b' {
		near = "c" + tok[1:]
	}
	cases := map[string]struct{ key, tok string }{
		"wrong token": {k, strings.Repeat("a", 32)},
		"near miss":   {k, near},
		"empty token": {k, ""},
		"blank token": {k, "  \n"},
		"bad key":     {"short", tok},
	}
	for name, c := range cases {
		if Matches(c.key, c.tok) {
			t.Errorf("%s: expected no match", name)
		}
	}
}

func TestLink(t *testing.T) {
	want := "https://example.com/setup?token=abc"
	for _, u := range []string{"https://example.com", "https://example.com/", "https://example.com//"} {
		if got := Link(u, "abc"); got != want {
			t.Errorf("Link(%q) = %q, want %q", u, got, want)
		}
	}
}
