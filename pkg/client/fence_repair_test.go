package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFenceRepairReviewsThenRepairsTheConnection(t *testing.T) {
	review := strings.Repeat("a", 64)
	refused := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		switch r.URL.EscapedPath() {
		case "/api/v1/connections/conn%2Fx/fence-repair/review":
			_, _ = w.Write([]byte(`{"daemon":"d","dirty":true,"review":"` + review + `","running":[{"container":"c","name":"app","exec":"e"}],"repaired":false}`))
		case "/api/v1/connections/conn%2Fx/fence-repair":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["review"] != review {
				t.Errorf("repair body %+v %v", body, err)
			}
			if refused {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"an exec is still running"}`))
				return
			}
			_, _ = w.Write([]byte(`{"daemon":"d","dirty":true,"review":"` + review + `","running":[],"repaired":true}`))
		default:
			t.Errorf("path %s", r.URL.EscapedPath())
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "test-key")
	got, err := c.ReviewFenceRepair(t.Context(), "conn/x")
	if err != nil || got.Review != review || string(got.Dirty) != "true" || len(got.Running) != 1 || got.Running[0].Name != "app" {
		t.Fatalf("review %+v %v", got, err)
	}
	repaired, err := c.RepairFence(t.Context(), "conn/x", review)
	if err != nil || !repaired.Repaired {
		t.Fatalf("repair %+v %v", repaired, err)
	}
	refused = true
	_, err = c.RepairFence(t.Context(), "conn/x", review)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Message != "an exec is still running" {
		t.Fatalf("refusal was lost: %v", err)
	}
}
