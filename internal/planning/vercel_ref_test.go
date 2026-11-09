package planning

import "testing"

func TestVercelRef(t *testing.T) {
	for _, c := range []struct {
		ref              string
		project, target  string
		native           bool
		fromProjectAndID bool
	}{
		{"prj_1#env_9", "prj_1", "env_9", true, true},
		{"prj_1#production", "prj_1", "production", true, true},
		{"prj_1#preview", "prj_1", "preview", true, true},
		{"prj_old", "prj_old", "", false, false},
		{"", "", "", false, false},
	} {
		project, target, native := ParseVercelRef(c.ref)
		if project != c.project || target != c.target || native != c.native {
			t.Errorf("ParseVercelRef(%q) = %q, %q, %v; want %q, %q, %v", c.ref, project, target, native, c.project, c.target, c.native)
		}
		if c.fromProjectAndID {
			if got := VercelRef(c.project, c.target); got != c.ref {
				t.Errorf("VercelRef(%q, %q) = %q, want %q", c.project, c.target, got, c.ref)
			}
		}
	}
}
