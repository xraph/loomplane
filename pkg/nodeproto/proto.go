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
	// CloseUnauthorized means the assertion was refused. Fetch a new challenge
	// and retry; if it keeps happening the node was revoked.
	CloseUnauthorized = 4401
	// CloseBadRequest means the first message was not a usable assertion.
	CloseBadRequest = 4400
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

// Fence carries the ownership claim a stream operates under.
type Fence struct {
	Resource   string `json:"resource"`
	Generation int64  `json:"generation"`
	Mode       string `json:"mode,omitempty"`
	Daemon     string `json:"daemon,omitempty"`
}

// StreamHeader opens every stream after stream 0. Arg, Mode and Daemon are
// carried in phase 1 and given meaning later.
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
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
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
