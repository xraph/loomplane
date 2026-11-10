package ctl

import (
	"net/http"
	"strings"
	"testing"
)

// fence-repair shows the review, and repairs only with --yes and the review
// it just read.
func TestFenceRepairNeedsTheExactReview(t *testing.T) {
	current := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name    string
		args    []string
		repairs int
		err     bool
	}{
		{name: "review_only", args: []string{"fence-repair", "conn"}},
		{name: "current", args: []string{"fence-repair", "conn", "--yes", "--review", current}, repairs: 1},
		{name: "old", args: []string{"fence-repair", "conn", "--yes", "--review", strings.Repeat("b", 64)}, err: true},
		{name: "no_review", args: []string{"fence-repair", "conn", "--yes"}, err: true},
		{name: "no_connection", args: []string{"fence-repair"}, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repairs := 0
			app, out := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/connections/conn/fence-repair/review":
					_, _ = w.Write([]byte(`{"daemon":"d","dirty":true,"review":"` + current + `","running":[],"repaired":false}`))
				case "/api/v1/connections/conn/fence-repair":
					repairs++
					_, _ = w.Write([]byte(`{"daemon":"d","dirty":true,"review":"` + current + `","running":[],"repaired":true}`))
				default:
					t.Errorf("path %s", r.URL.Path)
				}
			})
			err := app.Run(tc.args)
			if (err != nil) != tc.err || repairs != tc.repairs {
				t.Fatalf("err %v repairs %d", err, repairs)
			}
			if !tc.err && !strings.Contains(out.String(), current) {
				t.Fatalf("review not shown: %s", out.String())
			}
		})
	}
}
