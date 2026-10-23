package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
)

// containerNamePattern is what Docker accepts as a container name.
var containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// DialContainerPort opens a TCP connection to one port of a running
// container, through the SSH connection Dial pinned to the server's host
// key. It reaches the port where Docker publishes it on the server's
// loopback interface, which only the server can reach, so it never opens
// a listener of its own. A port published beyond loopback is refused: it's
// reachable by others, and isn't what a private component publishes.
func (c *Client) DialContainerPort(ctx context.Context, container string, port int) (net.Conn, error) {
	if !containerNamePattern.MatchString(container) || port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: no container port %s:%d", ErrInvalid, container, port)
	}
	var info containerInfo
	err := dockerJSON(ctx, c, http.MethodGet, "/containers/"+container+"/json", nil, &info)
	if errors.Is(err, ErrMissing) {
		return nil, fmt.Errorf("the container %s isn't on the server", container)
	}
	if err != nil {
		return nil, err
	}
	bindings := info.NetworkSettings.Ports[strconv.Itoa(port)+"/tcp"]
	if len(bindings) == 0 {
		return nil, fmt.Errorf("port %d isn't published on the server; is the container running?", port)
	}
	host := ""
	for _, b := range bindings {
		if b.HostIP != "127.0.0.1" {
			return nil, fmt.Errorf("port %d is published beyond loopback", port)
		}
		if value, convErr := strconv.Atoi(b.HostPort); convErr == nil && value > 0 && value <= 65535 && host == "" {
			host = b.HostPort
		}
	}
	if host == "" {
		return nil, fmt.Errorf("port %d isn't published on the server; is the container running?", port)
	}
	return c.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", host))
}
