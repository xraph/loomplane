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
	if got := c.Clouds.OAuth["digitalocean"]; got != (OAuthApp{"do-id", "do-secret"}) {
		t.Fatalf("digitalocean app = %+v", got)
	}

	_, err = FromSource(mapGetter{
		"database.url":                        testDSN,
		"clouds.oauth.digitalocean.client_id": "do-id",
	})
	if err == nil || !strings.Contains(err.Error(), "clouds.oauth.digitalocean.client_secret") {
		t.Fatalf("want an error naming client_secret, got %v", err)
	}

	c, err = FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatalf("FromSource: %v", err)
	}
	if len(c.Clouds.OAuth) != 0 {
		t.Fatalf("unset apps should be absent, got %+v", c.Clouds.OAuth)
	}
}
