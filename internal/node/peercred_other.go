//go:build !linux && !darwin

package node

import "net"

// peerAllowed has nothing to check elsewhere: the node refuses to run on
// systems other than Linux and macOS (Supported), and the socket is private.
func peerAllowed(net.Conn) (bool, error) { return true, nil }
