package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSetupContextFlag(t *testing.T) {
	if isSetup(context.Background()) {
		t.Fatal("a plain context counts as setup")
	}
	if !isSetup(withSetup(context.Background())) {
		t.Fatal("withSetup's context doesn't count as setup")
	}
}

func TestSetupDoneRule(t *testing.T) {
	down := errors.New("database is down")
	answer := func(v bool, err error) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) { return v, err }
	}
	tests := []struct {
		name               string
		recorded, hasUsers func(context.Context) (bool, error)
		want, wantErr      bool
	}{
		{"nothing recorded and no users", answer(false, nil), answer(false, nil), false, false},
		{"nothing recorded, users from before the record", answer(false, nil), answer(true, nil), true, false},
		{"recorded, every user gone", answer(true, nil), answer(false, nil), true, false},
		{"recorded, users not counted", answer(true, nil), answer(false, down), true, false},
		{"record unreadable", answer(false, down), answer(true, nil), false, true},
		{"users uncountable", answer(false, nil), answer(false, down), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setupDoneRule(t.Context(), tt.recorded, tt.hasUsers)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("setupDoneRule = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestOrgSlug(t *testing.T) {
	for name, want := range map[string]string{
		"Acme":               "acme",
		"  Acme  Corp, Ltd.": "acme-corp-ltd",
		"R&D 2":              "r-d-2",
		"Café":               "caf",
		"!!!":                "organization",
		"--a--":              "a",
	} {
		if got := orgSlug(name); got != want {
			t.Errorf("orgSlug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSplitName(t *testing.T) {
	for name, want := range map[string][2]string{
		"Ada Lovelace":        {"Ada", "Lovelace"},
		" Ada  King Lovelace": {"Ada", "King Lovelace"},
		"Ada":                 {"Ada", ""},
	} {
		if first, last := splitName(name); first != want[0] || last != want[1] {
			t.Errorf("splitName(%q) = %q, %q, want %q, %q", name, first, last, want[0], want[1])
		}
	}
}

func TestValidSetup(t *testing.T) {
	ok := SetupRequest{Name: " Ada ", Email: " Ada <ada@example.com> ", Password: "pw", OrganizationName: " Acme "}
	email, err := validSetup(&ok)
	if err != nil || email != "ada@example.com" || ok.Name != "Ada" || ok.OrganizationName != "Acme" {
		t.Fatalf("validSetup = %q, %v, form %+v", email, err, ok)
	}
	// 64 characters, not bytes: this is 64 runes and 128 bytes.
	if _, err := validSetup(&SetupRequest{Name: "Ada", Email: "ada@example.com", Password: "pw", OrganizationName: strings.Repeat("é", 64)}); err != nil {
		t.Fatalf("a 64-character name was refused: %v", err)
	}
	for name, in := range map[string]SetupRequest{
		"no name":     {Email: "ada@example.com", Password: "pw", OrganizationName: "Acme"},
		"blank name":  {Name: "  ", Email: "ada@example.com", Password: "pw", OrganizationName: "Acme"},
		"no email":    {Name: "Ada", Password: "pw", OrganizationName: "Acme"},
		"bad email":   {Name: "Ada", Email: "ada", Password: "pw", OrganizationName: "Acme"},
		"no password": {Name: "Ada", Email: "ada@example.com", OrganizationName: "Acme"},
		"no org":      {Name: "Ada", Email: "ada@example.com", Password: "pw"},
		"long org":    {Name: "Ada", Email: "ada@example.com", Password: "pw", OrganizationName: strings.Repeat("a", 65)},
	} {
		if _, err := validSetup(&in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
