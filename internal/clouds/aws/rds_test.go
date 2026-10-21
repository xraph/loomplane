package aws

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRDSRemovalRejectsUnpinnedRecovery(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	session := New(server.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")
	for _, spec := range []DBRemovalSpec{{ID: "database"}, {ID: "database", ResourceID: "db-original", SnapshotID: "different"}} {
		if _, err := session.DeleteDBInstance(t.Context(), spec, DBRemoval{}); err == nil {
			t.Fatal("removal without physical recovery pins was accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unpinned removal reached AWS")
	}
}
