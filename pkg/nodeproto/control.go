package nodeproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Hello is the first message a node sends on stream 0.
type Hello struct {
	Version     int    `json:"version"`
	NodeVersion string `json:"node_version"`
	Epoch       int64  `json:"epoch"`
}

// Heartbeat is a node's periodic liveness report.
type Heartbeat struct {
	At    time.Time `json:"at"`
	Usage Usage     `json:"usage"`
}

// InventoryReport carries a node's current inventory.
type InventoryReport struct {
	Inventory Inventory `json:"inventory"`
}

// Narrow asks a node to serve only these endpoints. Ack lists endpoints the
// node asked to allow with Allowed that the control plane has now decided on,
// whether it took them or not. The node stops treating them as pending, and
// Endpoints says which it kept.
type Narrow struct {
	Endpoints []Endpoint `json:"endpoints"`
	Ack       []Endpoint `json:"ack,omitempty"`
}

// NarrowApplied confirms the endpoints a node now serves.
type NarrowApplied struct {
	Endpoints []Endpoint `json:"endpoints"`
}

// SetIngress asks a node to change its ingress mode: "none", "direct" or "relay".
type SetIngress struct {
	Mode string `json:"mode"`
}

// IngressApplied confirms the ingress mode a node now runs.
type IngressApplied struct {
	Mode string `json:"mode"`
}

// Revoked tells a node the control plane no longer trusts it.
type Revoked struct{}

// Leave is sent by a node that asks to be revoked, as `node leave` does.
type Leave struct{}

// Allowed is sent by a node when its owner widens what it serves on the
// machine, as `node allow` does. The control plane adds these endpoints to
// the node's record; it never widens on its own.
type Allowed struct {
	Endpoints []Endpoint `json:"endpoints"`
}

// RotateKey replaces a node's key. Phase 5 uses it.
type RotateKey struct {
	NewPublicKey []byte `json:"new_public_key"`
	Signature    []byte `json:"signature"`
}

// Unknown is what DecodeControl returns for a type it does not know, so an
// older node can ignore messages from a newer control plane.
type Unknown struct {
	Type string `json:"type"`
}

// Usage is a node's resource use at one moment.
type Usage struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMiB int64   `json:"memory_used_mib"`
	DiskUsedGiB   int64   `json:"disk_used_gib"`
}

// Runtime describes a container runtime found on a node.
type Runtime struct {
	Socket  string `json:"socket"`
	Version string `json:"version"`
}

// GPU describes one graphics card on a node.
type GPU struct {
	Vendor    string `json:"vendor"`
	Model     string `json:"model"`
	MemoryMiB int64  `json:"memory_mib"`
}

// Inventory is what a node reports about its machine. Ports80443 is "free" or "busy".
type Inventory struct {
	Hostname        string     `json:"hostname,omitempty"`
	OS              string     `json:"os"`
	Kernel          string     `json:"kernel"`
	Arch            string     `json:"arch"`
	CPUs            int        `json:"cpus"`
	MemoryMiB       int64      `json:"memory_mib"`
	DiskGiB         int64      `json:"disk_gib"`
	Docker          *Runtime   `json:"docker,omitempty"`
	Podman          *Runtime   `json:"podman,omitempty"`
	GPUs            []GPU      `json:"gpus,omitempty"`
	Endpoints       []Endpoint `json:"endpoints,omitempty"`
	PublicAddresses []string   `json:"public_addresses,omitempty"`
	Ports80443      string     `json:"ports_80_443,omitempty"`
}

// controlTypes maps each wire type name to a constructor for its message.
var controlTypes = map[string]func() any{
	"hello":           func() any { return new(Hello) },
	"heartbeat":       func() any { return new(Heartbeat) },
	"inventory":       func() any { return new(InventoryReport) },
	"narrow":          func() any { return new(Narrow) },
	"narrow_applied":  func() any { return new(NarrowApplied) },
	"set_ingress":     func() any { return new(SetIngress) },
	"ingress_applied": func() any { return new(IngressApplied) },
	"revoked":         func() any { return new(Revoked) },
	"leave":           func() any { return new(Leave) },
	"allowed":         func() any { return new(Allowed) },
	"rotate_key":      func() any { return new(RotateKey) },
}

func typeName(msg any) string {
	switch msg.(type) {
	case Hello:
		return "hello"
	case Heartbeat:
		return "heartbeat"
	case InventoryReport:
		return "inventory"
	case Narrow:
		return "narrow"
	case NarrowApplied:
		return "narrow_applied"
	case SetIngress:
		return "set_ingress"
	case IngressApplied:
		return "ingress_applied"
	case Revoked:
		return "revoked"
	case Leave:
		return "leave"
	case Allowed:
		return "allowed"
	case RotateKey:
		return "rotate_key"
	}
	return ""
}

// EncodeControl writes msg as one line of JSON with a "type" field. msg must
// be one of the message structs, passed by value.
func EncodeControl(w io.Writer, msg any) error {
	name := typeName(msg)
	if name == "" {
		return fmt.Errorf("nodeproto: cannot encode %T as a control message", msg)
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// Every message marshals to an object, so splice the type in as its first field.
	var line bytes.Buffer
	line.WriteString(`{"type":"` + name + `"`)
	if len(body) > 2 {
		line.WriteByte(',')
		line.Write(body[1:])
	} else {
		line.WriteByte('}')
	}
	line.WriteByte('\n')
	_, err = w.Write(line.Bytes())
	return err
}

// DecodeControl parses one control line. A type it does not know decodes to
// Unknown with no error. Known types come back as values, not pointers.
func DecodeControl(line []byte) (any, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, err
	}
	if head.Type == "" {
		return nil, errors.New("nodeproto: control message has no type")
	}
	mk, ok := controlTypes[head.Type]
	if !ok {
		return Unknown{Type: head.Type}, nil
	}
	p := mk()
	if err := json.Unmarshal(line, p); err != nil {
		return nil, err
	}
	switch m := p.(type) {
	case *Hello:
		return *m, nil
	case *Heartbeat:
		return *m, nil
	case *InventoryReport:
		return *m, nil
	case *Narrow:
		return *m, nil
	case *NarrowApplied:
		return *m, nil
	case *SetIngress:
		return *m, nil
	case *IngressApplied:
		return *m, nil
	case *Revoked:
		return *m, nil
	case *Leave:
		return *m, nil
	case *Allowed:
		return *m, nil
	case *RotateKey:
		return *m, nil
	}
	return nil, fmt.Errorf("nodeproto: unhandled control type %q", head.Type)
}
