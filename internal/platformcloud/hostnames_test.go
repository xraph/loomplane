package platformcloud

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestGeneratedHostname(t *testing.T) {
	name, err := GeneratedHostname("web", "apps.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^web-[a-z2-7]{12}\.apps\.example\.com$`).MatchString(name) {
		t.Fatalf("hostname %q", name)
	}
	again, err := GeneratedHostname("web", "apps.example.com", nil)
	if err != nil || again == name {
		t.Fatalf("two calls gave %q and %q (%v)", name, again, err)
	}
	// The same randomness gives the same name, so the random part is all
	// that tells two services of one name apart.
	fixed := bytes.Repeat([]byte{0xff}, 8)
	a, _ := GeneratedHostname("web", "apps.example.com", bytes.NewReader(fixed))
	b, _ := GeneratedHostname("web", "apps.example.com", bytes.NewReader(fixed))
	if a != b || a != "web-777777777777.apps.example.com" {
		t.Fatalf("fixed randomness gave %q and %q", a, b)
	}
	// A long or awkward service name still makes one label of at most 63.
	for _, service := range []string{strings.Repeat("checkout-", 20), "Billing_API v2", "--", ""} {
		name, err = GeneratedHostname(service, "apps.example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		label, _, _ := strings.Cut(name, ".")
		if len(label) > 63 || !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?-[a-z2-7]{12}$`).MatchString(label) || strings.Contains(label, "--") {
			t.Fatalf("%q gave %q", service, name)
		}
	}
	if _, err = GeneratedHostname("web", "", nil); err == nil {
		t.Fatal("a hostname without an apex")
	}
}
