package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/engineprobe"
)

func TestCatalogReadinessRecursAndRefusesFailedAuthentication(t *testing.T) {
	iid := id.New(id.PrefixInstance)
	labels := map[string]string{owned.LabelOrg: "org_fixture", owned.LabelEnvironment: "env_fixture", owned.LabelProject: "proj_fixture", owned.LabelCatalogKind: "duckdb", owned.LabelHealthChecker: "duckdb-v1"}
	exitCode, runs := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			var body struct{ Cmd []string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			want, _ := engineprobe.Command("duckdb", "duckdb-v1")
			if !reflect.DeepEqual(body.Cmd, want) {
				t.Errorf("unexpected command: %v", body.Cmd)
			}
			runs++
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "exec_one"})
		case r.URL.Path == "/exec/exec_one/start":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/exec/exec_one/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"ExitCode": exitCode})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "container_one", "Config": map[string]any{"Labels": map[string]string{owned.LabelInstance: iid.String(), owned.LabelOrg: "org_fixture", owned.LabelProject: "proj_fixture", owned.LabelEnvironment: "env_fixture"}}, "State": map[string]any{"Running": true, "Status": "running"}})
		}
	}))
	defer api.Close()
	u, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{Config: &Config{Details: map[string]string{"docker_id": "daemon_fixture"}}, HTTP: &http.Client{Transport: runtimeTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return http.DefaultTransport.RoundTrip(r)
	})}}
	runtime := &Runtime{Wait: time.Nanosecond, Session: func(context.Context, map[string]string) (*Client, error) { return client, nil }, Probe: func(context.Context, *Client, int, string) error {
		t.Error("fell back to unauthenticated HTTP")
		return nil
	}}
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{0, 1, 0} {
		exitCode = code
		status, statusErr := runtime.Status(t.Context(), iid, labels)
		if statusErr != nil || status.Ready != (code == 0) {
			t.Fatalf("exit %d: %+v %v", code, status, statusErr)
		}
	}
	if runs != 4 {
		t.Fatalf("checker ran %d times", runs)
	}
	exitCode = 1
	if err = runtime.wait(t.Context(), client, ContainerName(iid), iid, labels); err == nil {
		t.Fatal("failed check passed startup")
	}
	labels[owned.LabelHealthChecker] = "unknown"
	before := runs
	if status, statusErr := runtime.Status(t.Context(), iid, labels); statusErr != nil || status.Ready {
		t.Fatalf("unknown checker: %+v %v", status, statusErr)
	}
	if runs != before {
		t.Fatal("executed unknown checker")
	}
}
