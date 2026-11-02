package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeCopyClientRetainsReviewedCloudSQLRecoveryNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"copy","target":{"provider":"cloud-sql","cloud_sql_network":{"consumer_project":"recovery-consumer","network":"projects/recovery-consumer/global/networks/worker"}}}`)
	}))
	defer server.Close()
	record, err := New(server.URL, "operator").NativeRecoveryCopy(t.Context(), "copy")
	if err != nil || record.Target.CloudSQLNetwork.ConsumerProject != "recovery-consumer" || record.Target.CloudSQLNetwork.Network != "projects/recovery-consumer/global/networks/worker" {
		t.Fatal("Go client lost the reviewed PSC consumer route", record, err)
	}
}
