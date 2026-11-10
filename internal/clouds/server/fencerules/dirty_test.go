package fencerules

import (
	"encoding/json"
	"strings"
	"testing"
)

// The journal's dirty mark is false, a bare true for an unknown Docker
// change, or the exec a start was running, as the SSH helper writes it. Any
// other mark reads as an unknown change, so it stays blocked.
func TestDirtyReadsBothForms(t *testing.T) {
	execution, container := strings.Repeat("b", 64), strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, journal string
		want          Dirty
		err           bool
	}{
		{name: "clean", journal: `{"epochs":{},"dirty":false}`},
		{name: "missing", journal: `{"epochs":{}}`},
		{name: "null", journal: `{"epochs":{},"dirty":null}`},
		{name: "legacy", journal: `{"epochs":{},"dirty":true}`, want: Dirty{Set: true}},
		{name: "exec", journal: `{"epochs":{},"dirty":{"mode":"exec","exec":"` + execution + `","container":"` + container + `"}}`, want: Dirty{Set: true, Exec: execution, Container: container}},
		{name: "other_mode", journal: `{"epochs":{},"dirty":{"mode":"request","exec":"` + execution + `","container":"` + container + `"}}`, want: Dirty{Set: true}},
		{name: "malformed_exec", journal: `{"epochs":{},"dirty":{"mode":"exec","exec":"../containers","container":"` + container + `"}}`, want: Dirty{Set: true}},
		{name: "malformed_container", journal: `{"epochs":{},"dirty":{"mode":"exec","exec":"` + execution + `","container":7}}`, want: Dirty{Set: true}},
		{name: "number", journal: `{"epochs":{},"dirty":1}`, err: true},
		{name: "string", journal: `{"epochs":{},"dirty":"yes"}`, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var j Journal
			err := json.Unmarshal([]byte(tc.journal), &j)
			if (err != nil) != tc.err {
				t.Fatalf("err %v, want error %v", err, tc.err)
			}
			if err == nil && j.Dirty != tc.want {
				t.Fatalf("dirty %+v, want %+v", j.Dirty, tc.want)
			}
		})
	}
}

// The node writes the marks the SSH helper writes.
func TestDirtyWritesTheHelperForms(t *testing.T) {
	execution, container := strings.Repeat("b", 64), strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		mark Dirty
		want string
	}{
		{name: "clean", want: `false`},
		{name: "unknown", mark: Dirty{Set: true}, want: `true`},
		{name: "exec", mark: ExecMark(execution, container), want: `{"mode":"exec","exec":"` + execution + `","container":"` + container + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(Journal{Epochs: map[string]int64{}, Dirty: tc.mark})
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Dirty json.RawMessage }
			if err = json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if string(got.Dirty) != tc.want {
				t.Fatalf("dirty %s, want %s", got.Dirty, tc.want)
			}
		})
	}
}
