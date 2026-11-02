package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// proxyDocker is a Docker daemon whose only container is the proxy for
// org/server running image, or none when image is empty.
func proxyDocker(t *testing.T, image string) *Client {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/"+IngressName("org", "server")+"/json" || image == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "proxy", "State": map[string]any{"Running": true}, "Config": map[string]any{"Image": image, "Labels": map[string]string{"loomplane.org": "org", "loomplane.ingress": "server"}}})
	}))
	t.Cleanup(api.Close)
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{Config: &Config{Details: map[string]string{}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}}
}

func TestIngressNeedsUpgradeReadsTheProxyImage(t *testing.T) {
	const dnsImage = "loomplane.test/ingress:dns"
	for _, tc := range []struct {
		name, pinned, running string
		upgrade               bool
		err                   error
	}{
		{name: "unpublished image, stock proxy", running: LegacyIngressImage, upgrade: true},
		{name: "unpublished image, no proxy yet", upgrade: true},
		{name: "stock proxy", pinned: dnsImage, running: LegacyIngressImage, upgrade: true},
		{name: "DNS-capable proxy", pinned: dnsImage, running: dnsImage},
		{name: "no proxy yet", pinned: dnsImage},
		{name: "foreign image", pinned: dnsImage, running: "caddy:latest", err: ErrOwned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinIngressImageForTest(t, tc.pinned)
			if IngressImagePinned() != (tc.pinned != "") {
				t.Fatal("pin")
			}
			upgrade, err := IngressNeedsUpgrade(t.Context(), proxyDocker(t, tc.running), "org", "server")
			if upgrade != tc.upgrade || !errors.Is(err, tc.err) {
				t.Fatalf("upgrade %v, err %v", upgrade, err)
			}
		})
	}
}

func TestUpgradeIngressNeedsThePublishedImage(t *testing.T) {
	pinIngressImageForTest(t, "")
	c := proxyDocker(t, LegacyIngressImage)
	c.RequireFence = true
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/server", Generation: 1})
	if _, err := UpgradeIngress(ctx, c, "org", "server", &IngressSnapshot{ContainerID: "proxy", ETag: "e", Config: json.RawMessage(`{}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatal("upgraded to an unpublished image", err)
	}
}

func TestWriteIngressDNSTokenRefusesUnsafeInput(t *testing.T) {
	c := proxyDocker(t, LegacyIngressImage)
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/ingress/server", Generation: 1})
	valid := "0123456789abcdefghijklmnopqrstuvwxyzABCD"
	for _, tc := range []struct{ zone, token string }{
		{"../etc", valid}, {"", valid}, {"Zone", valid}, {"zone_1", ""}, {"zone_1", valid + "\n"}, {"zone_1", "short"}, {"zone_1", "{file./etc/shadow}"},
	} {
		if err := WriteIngressDNSToken(ctx, c, "org", "server", tc.zone, []byte(tc.token)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q %q: %v", tc.zone, tc.token, err)
		}
	}
	if err := RemoveIngressDNSToken(ctx, c, "org", "server", "../etc"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
