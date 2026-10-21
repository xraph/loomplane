package cloudinfra

import (
	"errors"
	"testing"
)

func TestEnrollmentEndpointRequiresExplicitReachableTLS(t *testing.T) {
	for _, endpoint := range []string{"", "http://example.com", "https://localhost:8780", "https://127.0.0.1", "https://10.1.1.1", "https://user:pass@example.com", "https://example.com?token=x"} {
		s := NewService(Deps{EnrollmentURL: endpoint})
		if err := s.validateEnrollmentURL(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", endpoint, err)
		}
	}
	if err := NewService(Deps{EnrollmentURL: "https://enroll.example.com"}).validateEnrollmentURL(); err != nil {
		t.Fatal(err)
	}
	if err := NewService(Deps{EnrollmentURL: "http://127.0.0.1:1234", AllowLocalEnrollment: true}).validateEnrollmentURL(); err != nil {
		t.Fatal(err)
	}
}
