package planning

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAlias(t *testing.T) {
	for _, ok := range []string{"", "a", "shop-api", "api-prod-2", strings.Repeat("a", 63)} {
		if err := ValidateAlias(ok); err != nil {
			t.Errorf("ValidateAlias(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"Shop", "2api", "-api", "api-", "api--prod", "shop api", "shop_api", strings.Repeat("a", 64)} {
		if err := ValidateAlias(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateAlias(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}

// Whatever the suggestion is, it must be an alias the API would accept.
func TestSuggestAlias_isAValidAlias(t *testing.T) {
	for _, c := range []struct{ slug, env, name string }{
		{"shop", "production", "api"},
		{"shop", "staging", "orders-db"},
		{"", "production", "api"},
		{"shop", "", "api"},
		{"2fast", "pr-142", "9lives"},
		{strings.Repeat("long-project", 6), strings.Repeat("environment", 4), strings.Repeat("service", 6)},
	} {
		for _, subject := range []Subject{ServiceSubject, ResourceSubject} {
			got := SuggestAlias(c.slug, c.env, subject, c.name)
			if err := ValidateAlias(got); err != nil {
				t.Errorf("SuggestAlias(%q, %q, %s, %q) = %q: %v", c.slug, c.env, subject, c.name, got, err)
			}
		}
	}
}

func TestSuggestAlias(t *testing.T) {
	for _, c := range []struct{ slug, env, name, want string }{
		{"shop", "production", "api", "shop-api-prod"},
		{"shop", "staging", "orders-db", "shop-orders-db-stage"},
		{"shop", "pr-142", "api", "shop-api-pr-142"},
		{"", "production", "api", "api-prod"},
		{"2fast", "production", "api", "fast-api-prod"},
		{"shop", "production", "!!", ""},
		{strings.Repeat("a", 40), "production", strings.Repeat("b", 30), strings.Repeat("b", 30) + "-prod"},
	} {
		if got := SuggestAlias(c.slug, c.env, ServiceSubject, c.name); got != c.want {
			t.Errorf("SuggestAlias(%q, %q, %q) = %q, want %q", c.slug, c.env, c.name, got, c.want)
		}
	}
}
