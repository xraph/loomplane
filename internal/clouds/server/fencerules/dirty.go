package fencerules

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Dirty is the journal's dirty mark. The zero value is a clean journal. Set
// means a request may have left a Docker change in flight. Exec and
// Container name the exec start that set it, when it was one; empty, the
// mark is an unknown change that only an operator's reviewed repair clears.
//
// In JSON it is what the SSH helper writes: false, a bare true, or
// {"mode":"exec","exec":<id>,"container":<id>}. Any other object reads as
// an unknown change, so a mark nobody recognizes stays blocked.
type Dirty struct {
	Set             bool
	Exec, Container string
}

// ExecMark is the mark for an exec start of exec in container.
func ExecMark(exec, container string) Dirty {
	return Dirty{Set: true, Exec: exec, Container: container}
}

// MarshalJSON writes false, true or the exec object.
func (d Dirty) MarshalJSON() ([]byte, error) {
	switch {
	case !d.Set:
		return []byte("false"), nil
	case d.Exec == "":
		return []byte("true"), nil
	}
	return json.Marshal(struct {
		Mode      string `json:"mode"`
		Exec      string `json:"exec"`
		Container string `json:"container"`
	}{"exec", d.Exec, d.Container})
}

// UnmarshalJSON reads false or null as clean, true as an unknown change and
// an object as an exec mark when it is one with well-formed IDs.
func (d *Dirty) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	switch {
	case bytes.Equal(raw, []byte("null")), bytes.Equal(raw, []byte("false")):
		*d = Dirty{}
		return nil
	case bytes.Equal(raw, []byte("true")):
		*d = Dirty{Set: true}
		return nil
	case len(raw) == 0 || raw[0] != '{':
		return errors.New("the dirty mark is not false, true or an exec")
	}
	// The outer decoder has already checked the JSON, so an object that
	// doesn't decode has fields of the wrong types: an unknown change.
	var mark struct{ Mode, Exec, Container string }
	*d = Dirty{Set: true}
	if err := json.Unmarshal(raw, &mark); err == nil && mark.Mode == "exec" &&
		containerIDPattern.MatchString(mark.Exec) && containerIDPattern.MatchString(mark.Container) {
		d.Exec, d.Container = mark.Exec, mark.Container
	}
	return nil
}
