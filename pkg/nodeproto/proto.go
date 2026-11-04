// Package nodeproto is the versioned wire contract between Loomplane and the
// nodes that join it. It depends only on the standard library so both sides
// can import it.
package nodeproto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version is the protocol version a node announces in Hello.
const Version = 1

// WebSocket close codes the control plane sends a node before it hangs up.
// They sit in the private range (4000 to 4999).
const (
	// CloseUnauthorized means the node was revoked while it connected. A node
	// whose assertion fails is refused before the upgrade, with HTTP 401.
	CloseUnauthorized = 4401
)

// A node answers each stream's header with one verdict byte. Anything but
// VerdictAccepted is followed by a reason line ending in a newline. A
// reader takes a code it doesn't know for VerdictRefused, so a code added
// later stays within protocol Version 1.
const (
	VerdictAccepted byte = 0
	// VerdictRefused refuses the stream for the reason that follows.
	VerdictRefused byte = 1
	// VerdictNotAllowed refuses a stream whose endpoint the node doesn't
	// allow, which only its owner can change on the machine.
	VerdictNotAllowed byte = 2
)

// MaxHeaderSize is the largest stream header body, in bytes.
const MaxHeaderSize = 16 * 1024

// Endpoint names a kind of stream a node serves.
type Endpoint string

// The endpoints a stream header can ask for.
const (
	EndpointControl     Endpoint = "control"
	EndpointProbe       Endpoint = "probe"
	EndpointDocker      Endpoint = "docker"
	EndpointDial        Endpoint = "dial"
	EndpointKube        Endpoint = "kube"
	EndpointFirecracker Endpoint = "firecracker"
	EndpointPublic      Endpoint = "public"
)

// Valid reports whether e is a known endpoint.
func (e Endpoint) Valid() bool {
	switch e {
	case EndpointControl, EndpointProbe, EndpointDocker, EndpointDial,
		EndpointKube, EndpointFirecracker, EndpointPublic:
		return true
	}
	return false
}

// AllowableEndpoints returns the endpoints an owner can allow or narrow.
// Control is always on, and public is granted by ingress mode, so neither is
// in the list.
func AllowableEndpoints() []Endpoint {
	return []Endpoint{EndpointProbe, EndpointDocker, EndpointDial, EndpointKube, EndpointFirecracker}
}

// Ingress modes a node can run.
const (
	IngressNone   = "none"
	IngressDirect = "direct"
	IngressRelay  = "relay"
)

// Fence carries the ownership claim a stream operates under.
type Fence struct {
	Resource   string `json:"resource"`
	Generation int64  `json:"generation"`
	Mode       string `json:"mode,omitempty"`
	Daemon     string `json:"daemon,omitempty"`
}

// StreamHeader opens every stream after stream 0. Arg is a short probe
// argument, Fence.Mode one of the Fence* constants and Fence.Daemon the
// expected docker_id. See probes.go.
type StreamHeader struct {
	Endpoint    Endpoint `json:"endpoint"`
	Target      string   `json:"target,omitempty"`
	Arg         string   `json:"arg,omitempty"`
	OrgID       string   `json:"org_id,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
	Generation  int64    `json:"generation,omitempty"`
	Fence       *Fence   `json:"fence,omitempty"`
}

// ErrHeaderSize is returned for a header that is empty or over MaxHeaderSize.
var ErrHeaderSize = errors.New("nodeproto: bad header size")

// WriteHeader writes h as a 4-byte big-endian length followed by JSON.
func WriteHeader(w io.Writer, h StreamHeader) error {
	body, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(body) > MaxHeaderSize {
		return fmt.Errorf("%w: %d bytes", ErrHeaderSize, len(body))
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body))) //nolint:gosec // bounded by MaxHeaderSize above
	copy(buf[4:], body)
	_, err = w.Write(buf)
	return err
}

// ReadHeader reads one header written by WriteHeader. It checks the length
// before allocating, so a hostile peer cannot make it buffer more than
// MaxHeaderSize.
func ReadHeader(r io.Reader) (StreamHeader, error) {
	var h StreamHeader
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return h, err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > MaxHeaderSize {
		return h, fmt.Errorf("%w: %d bytes", ErrHeaderSize, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return h, err
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return StreamHeader{}, err
	}
	return h, nil
}
