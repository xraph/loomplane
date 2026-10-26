package config

import (
	"strings"
	"testing"
)

func TestOAuthAppsFromConfig(t *testing.T) {
	c, err := FromSource(mapGetter{
		"database.url":                            testDSN,
		"clouds.oauth.digitalocean.client_id":     "do-id",
		"clouds.oauth.digitalocean.client_secret": "do-secret",
	})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	if got := c.Clouds.OAuth["digitalocean"]; got != (OAuthApp{ClientID: "do-id", ClientSecret: "do-secret"}) {
		t.Fatalf("digitalocean app = %+v", got)
	}

	_, err = FromSource(mapGetter{
		"database.url":                        testDSN,
		"clouds.oauth.digitalocean.client_id": "do-id",
	})
	if err == nil || !strings.Contains(err.Error(), "clouds.oauth.digitalocean.client_secret") {
		t.Fatalf("want an error naming client_secret, got %v", err)
	}

	c, err = FromSource(mapGetter{
		"database.url":                      testDSN,
		"clouds.oauth.vercel.client_id":     "v-id",
		"clouds.oauth.vercel.client_secret": "v-secret",
		"clouds.oauth.vercel.slug":          "loomplane",
		"clouds.oauth.gcp.client_id":        "g-id",
		"clouds.oauth.gcp.client_secret":    "g-secret",
		"clouds.oauth.azure.client_id":      "a-id",
		"clouds.oauth.azure.client_secret":  "a-secret",
	})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	want := map[string]OAuthApp{
		"vercel": {ClientID: "v-id", ClientSecret: "v-secret", Slug: "loomplane"},
		"gcp":    {ClientID: "g-id", ClientSecret: "g-secret"},
		"azure":  {ClientID: "a-id", ClientSecret: "a-secret"},
	}
	for cloud, app := range want {
		if got := c.Clouds.OAuth[cloud]; got != app {
			t.Fatalf("%s app = %+v, want %+v", cloud, got, app)
		}
	}

	_, err = FromSource(mapGetter{
		"database.url":                      testDSN,
		"clouds.oauth.vercel.client_id":     "v-id",
		"clouds.oauth.vercel.client_secret": "v-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "clouds.oauth.vercel.slug") {
		t.Fatalf("want an error naming slug, got %v", err)
	}

	c, err = FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	if len(c.Clouds.OAuth) != 0 {
		t.Fatalf("unset apps should be absent, got %+v", c.Clouds.OAuth)
	}
}
