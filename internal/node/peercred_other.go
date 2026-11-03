//go:build !linux && !darwin

package node

import "net"

// peerUID has nothing to check elsewhere: the node refuses to run on
// systems other than Linux and macOS (Supported), and the socket is private.
// It answers root, as if the caller could do anything.
func peerUID(net.Conn) (uint32, error) { return 0, nil }
