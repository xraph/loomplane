package firecracker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSMTActive(t *testing.T) {
	cases := []struct {
		name          string
		files         map[string]string
		active, fails bool
	}{
		{"off", map[string]string{"active": "0\n"}, false, false},
		{"on", map[string]string{"active": "1\n"}, true, false},
		{"not supported", map[string]string{"control": "notsupported\n"}, false, false},
		{"unreadable state", map[string]string{"control": "on\n"}, false, true},
		{"nothing", map[string]string{}, false, true},
		{"garbage", map[string]string{"active": "yes\n"}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range c.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := smtActive(dir)
			if (err != nil) != c.fails || got != c.active {
				t.Fatal(got, err)
			}
		})
	}
}
