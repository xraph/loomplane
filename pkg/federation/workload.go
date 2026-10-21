package federation

import (
	"net/url"
	"path/filepath"
	"strings"
)

// ValidWorkloadAPIAddress restricts identity delivery to a canonical local socket.
func ValidWorkloadAPIAddress(address string) bool {
	return ValidUnixSocketAddress(address)
}

// ValidUnixSocketAddress accepts only canonical absolute local Unix endpoints.
func ValidUnixSocketAddress(address string) bool {
	socket, err := url.Parse(address)
	return err == nil && strings.HasPrefix(address, "unix:///") && socket.Scheme == "unix" && socket.Host == "" && socket.User == nil && socket.RawQuery == "" && !socket.ForceQuery && socket.Fragment == "" && filepath.IsAbs(socket.Path) && filepath.Clean(socket.Path) == socket.Path && socket.Path != "/" && socket.RawPath == "" && socket.String() == address
}
