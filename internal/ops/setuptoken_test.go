package ops

import "testing"

func TestSetupTokenOutput(t *testing.T) {
	const token = "abcdefghijklmnopqrstuvwxyz234567"
	tests := []struct {
		name      string
		publicURL string
		done, raw bool
		want      string
	}{
		{"done", "https://lp.example.com", true, false, "Setup is complete."},
		{"done raw", "https://lp.example.com", true, true, "Setup is complete."},
		{"raw", "https://lp.example.com", false, true, token},
		{"link", "https://lp.example.com", false, false, "Finish setup at https://lp.example.com/setup?token=" + token},
		{"link with trailing slash", "https://lp.example.com/", false, false, "Finish setup at https://lp.example.com/setup?token=" + token},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := setupTokenOutput(tt.publicURL, token, tt.done, tt.raw); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
