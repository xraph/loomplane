package ctl

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/client"
)

func TestCloudCapacityCommandsCallTheirRoutes(t *testing.T) {
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, []byte(`{"expected_revision":2,"policy":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pool := filepath.Join(dir, "pool.json")
	if err := os.WriteFile(pool, []byte(`{"name":"gpu","expected_revision":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args         []string
		method, path string
		body         string
	}{
		{[]string{"cloud-capacity", "get", "c1"}, "GET", "/api/v1/clouds/c1/capacity-policy", ""},
		{[]string{"cloud-capacity", "set", "c1", "--file", policy}, "POST", "/api/v1/clouds/c1/capacity-policy", `"expected_revision":2`},
		{[]string{"cloud-capacity", "usage", "c1"}, "GET", "/api/v1/clouds/c1/capacity-usage", ""},
		{[]string{"cloud-capacity", "refresh", "c1"}, "POST", "/api/v1/clouds/c1/capacity-usage/refresh", ""},
		{[]string{"cloud-capacity", "expansions", "c1"}, "GET", "/api/v1/clouds/c1/expansions", ""},
		{[]string{"cloud-capacity", "approve", "c1", "e1", "--revision", "3", "--yes"}, "POST", "/api/v1/clouds/c1/expansions/e1/approve", `{"expected_revision":3}`},
		{[]string{"cloud-capacity", "create-pool", "c1", "--file", pool}, "POST", "/api/v1/clouds/c1/scaling-pools", `"name":"gpu"`},
		{[]string{"resume", "d1", "--revision", "4"}, "POST", "/api/v1/deployments/d1/capacity/resume", `{"expected_revision":4}`},
		{[]string{"cancel", "d1", "--revision", "4"}, "POST", "/api/v1/deployments/d1/capacity/cancel", `{"expected_revision":4}`},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var method, path, body string
			app, _ := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				method, path, body = r.Method, r.URL.Path, string(b)
				_, _ = w.Write([]byte(`{"id":"x","expansions":[],"status":"running"}`))
			})
			if err := app.Run(tc.args); err != nil {
				t.Fatal(err)
			}
			if method != tc.method || path != tc.path || !strings.Contains(body, tc.body) {
				t.Fatalf("got %s %s %s", method, path, body)
			}
		})
	}
}

func TestCloudCapacitySetNeedsExpectedRevision(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(file, []byte(`{"policy":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	app, _ := infraTestApp(t, "", func(http.ResponseWriter, *http.Request) { t.Error("requested") })
	if err := app.Run([]string{"cloud-capacity", "set", "c1", "--file", file}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestCloudCapacityStaleRevisionIsExplained(t *testing.T) {
	app, _ := infraTestApp(t, "", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) })
	err := app.Run([]string{"resume", "d1", "--revision", "1"})
	if err == nil || !strings.Contains(err.Error(), "changed since you opened it") {
		t.Fatalf("err=%v", err)
	}
}

func TestBlockLineGPU(t *testing.T) {
	got := blockLine(client.CapacityBlock{Reason: "hard_limit", Resource: "gpus:nvidia-l4", Limit: 2, Counted: 2, Requested: 1})
	want := "NVIDIA L4 GPUs is at this Cloud's limit: 2 of 2 counted, 1 more needed."
	if got != want {
		t.Fatalf("got %q", got)
	}
	if got := blockLine(client.CapacityBlock{Reason: "weird_thing"}); got != "weird thing" {
		t.Fatalf("got %q", got)
	}
}

func TestBlockLineUsesUsageUnits(t *testing.T) {
	for _, tc := range []struct {
		block client.CapacityBlock
		want  string
	}{
		{client.CapacityBlock{Reason: "hard_limit", Resource: "cpu_millis", Counted: 4000, Limit: 8000, Requested: 500}, "CPU is at this Cloud's limit: 4 cores of 8 cores counted, 0.5 cores more needed."},
		{client.CapacityBlock{Reason: "hard_limit", Resource: "memory_bytes", Counted: 6 << 30, Limit: 8 << 30, Requested: 3 << 30}, "Memory is at this Cloud's limit: 6.0 GiB of 8.0 GiB counted, 3.0 GiB more needed."},
		{client.CapacityBlock{Reason: "hard_limit", Resource: "servers", Counted: 3, Limit: 3, Requested: 1}, "Servers is at this Cloud's limit: 3 of 3 counted, 1 more needed."},
		{client.CapacityBlock{Reason: "hard_limit", Resource: "servers", PoolID: "p", Counted: 2, Limit: 2, Requested: 1}, "Servers is at this pool's limit: 2 of 2 counted, 1 more needed."},
	} {
		if got := blockLine(tc.block); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// With automatic capacity off, a deployment that needs a server is not a plan
// change: the line says no server will be added and what to do.
func TestBlockLineRolloutOff(t *testing.T) {
	got := blockLine(client.CapacityBlock{Reason: "review_required", Resource: "rollout"})
	if got != "Automatic expansion is off on this Loomplane server, so no server will be added. Add or adopt one yourself." {
		t.Fatalf("got %q", got)
	}
	if got := blockLine(client.CapacityBlock{Reason: "review_required"}); got != "This deployment's plan changed. Review it again." {
		t.Fatalf("plan change: got %q", got)
	}
}

func TestUsageHumanShowsUnknown(t *testing.T) {
	app, out := infraTestApp(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"provisioned":{"servers":3,"cpu_millis":null},"gpus":[{"kind":"nvidia-l4","provisioned":null,"reserved":1,"limit":null}],"fresh":true}`))
	})
	if err := app.Run([]string{"cloud-capacity", "usage", "c1", "--human"}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"Servers: provisioned 3", "CPU: provisioned unknown", "nvidia-l4 GPUs: provisioned unknown, reserved 1, limit unknown"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "provisioned 0") {
		t.Errorf("unknown printed as zero:\n%s", s)
	}
}

func TestExpansionsHumanPrices(t *testing.T) {
	app, out := infraTestApp(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"expansions":[{"id":"e1","status":"awaiting_approval","pool_id":"p","size":"gpu-l4","resources":{"gpu_kind":"nvidia-l4","gpus":1},"hourly_price_known":true,"estimated_hourly_usd":3.39},{"id":"e2","status":"awaiting_approval","pool_id":"p","size":"s-2"}]}`))
	})
	if err := app.Run([]string{"cloud-capacity", "expansions", "c1", "--human"}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "e1 awaiting_approval p gpu-l4 1xnvidia-l4 $3.39/hr") || !strings.Contains(s, "e2 awaiting_approval p s-2 no-gpu price unknown") {
		t.Fatalf("got\n%s", s)
	}
}

func TestFollowStopsAtPausedWithResumeHint(t *testing.T) {
	app, out := infraTestApp(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"d1","status":"paused","capacity_wait":{"revision":5,"blocks":[{"reason":"hard_limit","resource":"servers","limit":3,"counted":3,"requested":1}]}}`))
	})
	if err := app.Run([]string{"deployment", "d1", "--watch"}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "Servers is at this Cloud's limit: 3 of 3 counted, 1 more needed.") || !strings.Contains(s, "loomctl resume d1 --revision 5") {
		t.Fatalf("got\n%s", s)
	}
}

func TestCapacityRefusalsKeepTheServersReason(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error":"a deployment step is busy"}`, "busy"},
		{`{"error":"the deployment is already cancelled"}`, "already cancelled"},
		{``, "changed since you opened it"},
	} {
		app, _ := infraTestApp(t, "", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(tc.body))
		})
		err := app.Run([]string{"cancel", "d1", "--revision", "1"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("body %q: err=%v, want %q", tc.body, err, tc.want)
		}
	}
}

func TestApproveShowsPriceBeforeAskingAndDeclineSendsNothing(t *testing.T) {
	for _, tc := range []struct{ expansion, want string }{
		{`{"id":"e1","status":"awaiting_approval","pool_id":"p","size":"gpu-l4","hourly_price_known":true,"estimated_hourly_usd":3.39}`, "$3.39/hr"},
		{`{"id":"e1","status":"awaiting_approval","pool_id":"p","size":"gpu-l4"}`, "price unknown"},
	} {
		posts := 0
		answerNo(t)
		app, out := infraTestApp(t, "", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts++
			}
			_, _ = w.Write([]byte(`{"expansions":[` + tc.expansion + `]}`))
		})
		err := app.Run([]string{"cloud-capacity", "approve", "c1", "e1", "--revision", "3"})
		s := out.String()
		if err != nil || !strings.Contains(s, "Nothing approved.") {
			t.Errorf("declined without saying so: err=%v\n%s", err, s)
		}
		if !strings.Contains(s, tc.want) || posts != 0 {
			t.Errorf("want %q before asking, posts=%d err=%v:\n%s", tc.want, posts, err, s)
		}
	}
}

// answerNo makes the confirmation prompt read "n" from standard input.
func answerNo(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("n\n")
	_ = w.Close()
	previous := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = previous; _ = r.Close() })
}
