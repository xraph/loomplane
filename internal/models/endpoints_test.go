package models

import (
	"strings"
	"testing"
)

func TestProviderName(t *testing.T) {
	if got := ProviderName("llama-8b"); got != "model-llama-8b" {
		t.Errorf("short name: %q", got)
	}
	exact := strings.Repeat("a", 57) // "model-" and 57 is 63
	if got := ProviderName(exact); got != "model-"+exact {
		t.Errorf("63 characters: %q", got)
	}
	long1, long2 := strings.Repeat("b", 70)+"-one", strings.Repeat("b", 70)+"-two"
	a, b := ProviderName(long1), ProviderName(long2)
	if len(a) != 63 || len(b) != 63 || a == b {
		t.Errorf("long names: %q (%d), %q (%d)", a, len(a), b, len(b))
	}
	if !strings.HasPrefix(a, "model-bbbb") || a[56] != '-' || strings.Trim(a[57:], "0123456789abcdef") != "" {
		t.Errorf("long name %q doesn't end with -<6 hex>", a)
	}
	if ProviderName(long1) != a {
		t.Error("the same resource got two names")
	}
}
