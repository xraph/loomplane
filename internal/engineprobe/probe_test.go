package engineprobe

import (
	"reflect"
	"testing"
)

func TestCommandsAreFixedAndKindBound(t *testing.T) {
	for _, kind := range []string{"falkordb", "elasticsearch", "opensearch", "duckdb"} {
		cmd, err := Command(kind, kind+"-v1")
		if err != nil || len(cmd) == 0 {
			t.Fatalf("%s: %v %v", kind, cmd, err)
		}
		again, _ := Command(kind, kind+"-v1")
		cmd[0] = "changed"
		if reflect.DeepEqual(cmd, again) {
			t.Fatal("command shares mutable storage")
		}
		if _, err = Command("other", kind+"-v1"); err == nil {
			t.Fatal("accepted another kind's checker")
		}
	}
	for _, name := range []string{"", "sh -c anything", "duckdb-v2", "/bin/sh"} {
		if _, err := Command("duckdb", name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
