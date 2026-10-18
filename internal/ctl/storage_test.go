package ctl

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestStorageCommandReadsCheckedConnectionInventory(t *testing.T) {
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/connections/conn_one/storage" {
			t.Fatalf("unexpected storage request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"volumes":[{"id":"conn_one:volume","name":"volume","connection_id":"conn_one","project_id":"project","environment_id":"env","instance_id":"instance","role":"data","provider":"server","status":"retained","capacity_bytes":0}]}`))
	})
	if err := app.Run([]string{"storage", "conn_one", "--json"}); err != nil {
		t.Fatal(err)
	}
	var volumes []struct {
		Name, Status  string
		CapacityBytes int64 `json:"capacity_bytes"`
	}
	if err := json.Unmarshal(out.Bytes(), &volumes); err != nil || len(volumes) != 1 || volumes[0].Status != "retained" {
		t.Fatalf("storage JSON: %s %v", out, err)
	}
}
