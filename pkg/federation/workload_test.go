package federation

import "testing"

func TestWorkloadAPIAddress(t *testing.T) {
	for _, address := range []string{"unix:///run/spire/agent.sock", "unix:///tmp/agent.sock"} {
		if !ValidWorkloadAPIAddress(address) {
			t.Errorf("rejected local socket %q", address)
		}
	}
	for _, address := range []string{"", "unix:/run/agent.sock", "unix:///", "unix://host/run/agent.sock", "unix:///run/../agent.sock", "unix:///run//agent.sock", "unix:///run/agent.sock?", "unix:///run/agent.sock#", "unix:///run/%61gent.sock", "http://localhost:8081", "tcp://127.0.0.1:8081"} {
		if ValidWorkloadAPIAddress(address) {
			t.Errorf("accepted noncanonical socket %q", address)
		}
	}
}
