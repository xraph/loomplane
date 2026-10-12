package discovery

import (
	"errors"
	"strings"
	"testing"
)

func TestValidAlias(t *testing.T) {
	tests := []struct {
		alias string
		want  error
	}{
		{"web", nil},
		{"api-2", nil},
		{"a", nil},
		{"abcdefghijklmnopqrstuvwxyz012345", nil},
		{"abcdefghijklmnopqrstuvwxyz0123456", ErrInvalidSource},
		{"Web", ErrInvalidSource},
		{"2web", ErrInvalidSource},
		{"", ErrInvalidSource},
		{"go", ErrAliasReserved},
		{"container", ErrAliasReserved},
	}
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			err := ValidAlias(tt.alias)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("ValidAlias(%q) = %v, want %v", tt.alias, err, tt.want)
			}
		})
	}
}

func TestAliasFor(t *testing.T) {
	tests := []struct{ name, want string }{
		{"storefront", "storefront"},
		{"Storefront.git", "storefront"},
		{"my_cool.Repo", "my-cool-repo"},
		{"go", "go-src"},
		{"node", "node-src"},
		{"123-api", "r-123-api"},
		{"---", "repo"},
		{strings.Repeat("a", 40), strings.Repeat("a", 32)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AliasFor(tt.name)
			if got != tt.want {
				t.Fatalf("AliasFor(%q) = %q, want %q", tt.name, got, tt.want)
			}
			if err := ValidAlias(got); err != nil {
				t.Fatalf("AliasFor(%q) gave an invalid alias: %v", tt.name, err)
			}
		})
	}
}

func TestCleanRoot(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{"", "", false},
		{"/", "", false},
		{".", "", false},
		{" apps/web/ ", "apps/web", false},
		{"apps//web", "", true},
		{"../x", "", true},
		{"a/../b", "", true},
		{"..", "", true},
		{`a\b`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := CleanRoot(tt.in)
			if tt.bad {
				if !errors.Is(err, ErrInvalidSource) {
					t.Fatalf("CleanRoot(%q) = %q, %v; want ErrInvalidSource", tt.in, got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("CleanRoot(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestRootsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "apps", true},
		{"apps", "apps", true},
		{"apps", "apps/web", true},
		{"apps/web", "apps", true},
		{"apps/web", "apps/webby", false},
		{"apps", "services", false},
	}
	for _, tt := range tests {
		if got := RootsOverlap(tt.a, tt.b); got != tt.want {
			t.Errorf("RootsOverlap(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

func TestParseRepository(t *testing.T) {
	for _, in := range []string{"acme/mono", " https://github.com/acme/mono.git ", "git@github.com:acme/mono.git", "github.com/acme/mono"} {
		repo, err := ParseRepository(in)
		if err != nil || repo.FullName() != "acme/mono" {
			t.Errorf("ParseRepository(%q) = %v, %v", in, repo, err)
		}
	}
	for _, in := range []string{"https://gitlab.com/acme/mono", "acme", ""} {
		if _, err := ParseRepository(in); err == nil {
			t.Errorf("ParseRepository(%q) succeeded", in)
		}
	}
}
