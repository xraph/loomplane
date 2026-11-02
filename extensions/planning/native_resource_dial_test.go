package planning

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/connections"
)

func TestNativeResourceChecksUseGuardedControlPlaneDial(t *testing.T) {
	for _, cloud := range []string{connections.DigitalOcean, connections.Azure} {
		t.Run(cloud, func(t *testing.T) {
			creds := dialCredentials{"target": {ID: "target", OrgID: "org", Cloud: cloud, Status: connections.StatusReady}}
			d := ownedDialer{credentials: creds}
			dial, closer, err := d.TargetDial(t.Context(), "org", "target")
			if err != nil || dial == nil {
				t.Fatalf("native external resource verifier unavailable: %v", err)
			}
			if closer != nil {
				t.Cleanup(func() { _ = closer.Close() })
			}
			if conn, err := dial(t.Context(), "tcp", "127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "Loomplane doesn't connect") {
				if conn != nil {
					_ = conn.Close()
				}
				t.Fatalf("native verifier bypassed the control-plane network guard: %v", err)
			}
		})
	}
}

func TestNativeResourceChecksRequireCurrentScopedTarget(t *testing.T) {
	for _, scenario := range []string{"foreign-org", "wrong-id", "not-ready"} {
		t.Run(scenario, func(t *testing.T) {
			conn := &connections.Connection{ID: "target", OrgID: "org", Cloud: connections.Azure, Status: connections.StatusReady}
			switch scenario {
			case "foreign-org":
				conn.OrgID = "foreign"
			case "wrong-id":
				conn.ID = "substituted"
			case "not-ready":
				conn.Status = "error"
			}
			d := ownedDialer{credentials: dialCredentials{"target": conn}}
			if dial, closer, err := d.TargetDial(t.Context(), "org", "target"); err == nil || dial != nil || closer != nil {
				t.Fatalf("invalid native target granted resource checks: %v", err)
			}
		})
	}
}
