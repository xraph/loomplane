package nativepg

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestSourceProbeRequiresGuardAndExplicitIdentityBeforeDial(t *testing.T) {
	for _, scenario := range []string{"guard", "database", "user", "password", "version"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			verifier := &Verifier{Dial: func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("unexpected dial")
			}}
			spec := Spec{Database: "database", AdminUser: "app", AdminPassword: "kept-password", MajorVersion: 17}
			guard := func(context.Context) error { return nil }
			switch scenario {
			case "guard":
				guard = nil
			case "database":
				spec.Database = ""
			case "user":
				spec.AdminUser = ""
			case "password":
				spec.AdminPassword = ""
			case "version":
				spec.MajorVersion = 16
			}
			if _, err := verifier.Probe(t.Context(), spec, guard); err == nil || calls != 0 {
				t.Fatal("invalid probe reached transport", err, calls)
			}
		})
	}
}
