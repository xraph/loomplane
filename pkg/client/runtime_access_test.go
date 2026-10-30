package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestRuntimeAccessOmittedJSON(t *testing.T) {
	for _, value := range []any{ClusterInput{}, AttachmentInput{}} {
		raw, err := json.Marshal(value)
		if err != nil || strings.Contains(string(raw), "runtime_access") {
			t.Fatal("historical omission changed", err)
		}
	}
	in := ClusterInput{Kind: "runtime-access-upgrade", RuntimeAccess: &provider.RuntimeAccessInput{AttachmentID: "attachment", Profile: provider.RuntimeAccessProfile{SchemaVersion: 1, CleanupDependents: true}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var recovered ClusterInput
	if err = json.Unmarshal(raw, &recovered); err != nil || recovered.RuntimeAccess == nil || *recovered.RuntimeAccess != *in.RuntimeAccess {
		t.Fatal("profile roundtrip", err)
	}
}
