package nativepg

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSpecKeepsPasswordsOutOfSerializedMetadata(t *testing.T) {
	raw, err := json.Marshal(Spec{Database: "app", AdminPassword: "private-admin-password", ReaderPassword: "private-reader-password"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-admin-password") || strings.Contains(string(raw), "private-reader-password") {
		t.Fatal("SQL credentials leaked into serialized metadata")
	}
}
