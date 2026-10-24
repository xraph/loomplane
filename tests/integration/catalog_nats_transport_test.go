//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Docker Desktop does not route container IPs from macOS. The SSH fixture
// bridges the exact inspected NATS container through its native TCP socket.
func fixtureNATSTCP(t *testing.T, state infrastructureState, request ssh.NewChannel, host string, port uint32) bool {
	if !state.HostLoopback || net.ParseIP(host) == nil || host == "127.0.0.1" || port != 4222 {
		return false
	}
	ids := strings.Fields(fixtureCommand(t, "docker", "--host", state.DockerHost, "ps", "-q", "--filter", "label=loomplane.nats.engine=nats"))
	for _, container := range ids {
		raw := fixtureCommand(t, "docker", "--host", state.DockerHost, "inspect", container, "--format", "{{json .NetworkSettings.Networks}}")
		var networks map[string]struct{ IPAddress string }
		if json.Unmarshal([]byte(raw), &networks) != nil {
			continue
		}
		for _, network := range networks {
			if network.IPAddress != host {
				continue
			}
			ch, requests, err := request.Accept()
			if err != nil {
				return true
			}
			go ssh.DiscardRequests(requests)
			go func() {
				defer ch.Close()
				command := fixtureDockerCommand(t.Context(), state, "exec", "-i", container, "nc", "127.0.0.1", strconv.FormatUint(uint64(port), 10))
				command.Stdin, command.Stdout, command.Stderr = ch, ch, io.Discard
				_ = command.Run()
			}()
			return true
		}
	}
	return false
}
