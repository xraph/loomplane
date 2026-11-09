package instance

import "testing"

func TestEffectiveWaitlist(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name     string
		override *bool
		on       bool
		stored   bool
		want     bool
	}{
		{"nothing stored, no override", nil, false, false, false},
		{"stored on", nil, true, true, true},
		{"override off beats stored on", &no, true, true, false},
		{"override on beats nothing stored", &yes, false, false, true},
		{"on without a stored row is off", nil, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveWaitlist(tt.override, tt.on, tt.stored); got != tt.want {
				t.Fatalf("EffectiveWaitlist = %v, want %v", got, tt.want)
			}
		})
	}
}
