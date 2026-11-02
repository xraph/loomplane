package recovery

import (
	"encoding/json"
	"testing"
)

func TestNativeCloudSQLTargetKeepsOriginalCheckpointEncoding(t *testing.T) {
	target := NativeRestoreTarget{Provider: "cloud-sql", ConnectionID: "connection", AccountIdentity: "account", NativeProject: "recovery-project", Region: "us-east1", CloudID: "cloud", TargetID: "target", CloudRevision: 1, TargetRevision: 2, GrantRevision: 3}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	// Target encoding participates in the immutable copy scope. Extra zero RDS
	// fields would invalidate Cloud SQL credentials/checkpoints already retained.
	want := `{"provider":"cloud-sql","connection_id":"connection","account_identity":"account","native_project":"recovery-project","region":"us-east1","cloud_id":"cloud","target_id":"target","cloud_revision":1,"target_revision":2,"grant_revision":3}`
	if string(raw) != want {
		t.Fatal("RDS changed existing Cloud SQL checkpoint scope encoding", string(raw))
	}
}
