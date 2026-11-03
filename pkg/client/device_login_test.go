package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeAuthsome answers the public config and authsome's device and token
// endpoints, giving the token endpoint's answers in turn.
func fakeAuthsome(t *testing.T, answers ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var grants []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/public-config", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"auth_base_path": "/authsome"})
	})
	mux.HandleFunc("POST /authsome/v1/oauth/device/authorize", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != CLIClientID {
			http.Error(w, "bad client", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-123", "user_code": "BCDF-GHJK",
			"verification_uri": "https://lp.example/device", "verification_uri_complete": "https://lp.example/device?code=BCDF-GHJK",
			"expires_in": 600, "interval": 5,
		})
	})
	mux.HandleFunc("POST /authsome/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		grants = append(grants, r.PostForm.Get("grant_type")+" "+r.PostForm.Get("device_code")+r.PostForm.Get("refresh_token"))
		if len(answers) == 0 {
			t.Error("the token endpoint was asked more often than expected")
			return
		}
		next := answers[0]
		answers = answers[1:]
		w.Header().Set("Content-Type", "application/json")
		if next == "ok" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "refresh_token": "ref", "expires_in": 3600})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": next, "error_description": next + " said the server"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &grants
}

func TestDeviceLoginWaitsAtTheServersPace(t *testing.T) {
	srv, grants := fakeAuthsome(t, "authorization_pending", "slow_down", "authorization_pending", "ok")
	login, err := startDeviceLogin(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if login.UserCode != "BCDF-GHJK" || login.VerificationURI != "https://lp.example/device" || login.Base != srv.URL {
		t.Fatalf("login %+v", login)
	}
	var waits []time.Duration
	login.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	tok, err := login.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "tok" || tok.RefreshToken != "ref" || time.Until(tok.ExpiresAt) < 59*time.Minute {
		t.Fatalf("token %+v", tok)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waited %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waited %v, want %v", waits, want)
		}
	}
	if (*grants)[0] != deviceCodeGrant+" dev-123" {
		t.Fatalf("polled with %q", (*grants)[0])
	}
}

func TestDeviceLoginStopsWhenDeniedOrExpired(t *testing.T) {
	for answer, want := range map[string]error{"access_denied": ErrDeviceDenied, "expired_token": ErrDeviceExpired} {
		srv, _ := fakeAuthsome(t, "authorization_pending", answer)
		login, err := startDeviceLogin(context.Background(), srv.Client(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		login.sleep = func(context.Context, time.Duration) error { return nil }
		if _, err := login.Wait(context.Background()); !errors.Is(err, want) {
			t.Fatalf("%s: err %v, want %v", answer, err, want)
		}
	}
}

func TestDeviceLoginStopsWhenTheContextEnds(t *testing.T) {
	srv, _ := fakeAuthsome(t)
	login, err := startDeviceLogin(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := login.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
