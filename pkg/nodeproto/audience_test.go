package nodeproto

import "testing"

func TestNormalizeAudience(t *testing.T) {
	ok := map[string]string{
		"https://Loom.Example.COM":          "https://loom.example.com",
		"HTTPS://loom.example.com/":         "https://loom.example.com",
		"https://loom.example.com:443":      "https://loom.example.com",
		"http://loom.example.com:80/":       "http://loom.example.com",
		"https://loom.example.com:8443":     "https://loom.example.com:8443",
		"http://localhost:8080":             "http://localhost:8080",
		"  https://loom.example.com/lp//  ": "https://loom.example.com/lp",
		"https://[::1]:443":                 "https://[::1]",
		"https://[::1]:9000/":               "https://[::1]:9000",
	}
	for in, want := range ok {
		got, err := NormalizeAudience(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "loom.example.com", "ftp://loom.example.com", "https://", "https://user@loom.example.com", "https://loom.example.com/?a=b", "https://loom.example.com/#x"} {
		if got, err := NormalizeAudience(bad); err == nil {
			t.Errorf("%q: accepted as %q", bad, got)
		}
	}
}
