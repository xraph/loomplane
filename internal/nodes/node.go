// Package nodes stores the machines that join an org and hold one outbound
// connection to Loomplane.
package nodes

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// Status is where a node is in its life.
type Status string

// The statuses a node moves through.
const (
	StatusPending  Status = "pending"
	StatusOnline   Status = "online"
	StatusOffline  Status = "offline"
	StatusDraining Status = "draining"
	StatusRevoked  Status = "revoked"
)

var (
	// ErrNotFound is returned when no node or session matches.
	ErrNotFound = errors.New("nodes: not found")
	// ErrNameTaken is returned when a live node of the org already has the name.
	ErrNameTaken = errors.New("nodes: name taken")
	// ErrWidenRemotely is returned when an update would add endpoints. Only
	// the machine's owner can widen what it serves.
	ErrWidenRemotely = errors.New("add endpoints on the machine with: loomctl node allow <endpoint>")
)

// Node is one machine in an org.
type Node struct {
	ID                 string               `json:"id"`
	OrgID              string               `json:"org_id"`
	Name               string               `json:"name"`
	PublicKey          []byte               `json:"-"`
	KeyFingerprint     string               `json:"key_fingerprint"`
	IdentityGeneration int64                `json:"identity_generation"`
	Status             Status               `json:"status"`
	Endpoints          []nodeproto.Endpoint `json:"endpoints"`
	IngressMode        string               `json:"ingress_mode"` // "none", "direct" or "relay"
	Role               string               `json:"role"`         // "node" or "edge"
	EdgeScope          string               `json:"edge_scope"`   // "", "org" or "instance"
	Labels             map[string]string    `json:"labels"`
	Version            string               `json:"version"`
	ProtocolVersion    int                  `json:"protocol_version"`
	Inventory          nodeproto.Inventory  `json:"inventory"`
	LastSeenAt         *time.Time           `json:"last_seen_at"`
	JoinedBy           string               `json:"joined_by"`
	JoinedByKeyID      string               `json:"joined_by_key_id"`
	RevokedAt          *time.Time           `json:"revoked_at"`
	RevokedBy          string               `json:"revoked_by"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

// SettingsUpdate is the part of a node an owner may change remotely. A nil
// field is left alone; a non-nil empty slice or map is a real change.
type SettingsUpdate struct {
	Name        *string
	Labels      map[string]string
	IngressMode *string
	Endpoints   []nodeproto.Endpoint
}

// Session is the live connection a replica holds for a node. A newer epoch
// fences an older connection.
type Session struct {
	NodeID      string    `json:"node_id"`
	Epoch       int64     `json:"epoch"`
	Replica     string    `json:"replica"`
	RemoteAddr  string    `json:"remote_addr"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Store persists nodes and their sessions.
type Store interface {
	Create(ctx context.Context, n *Node) error
	Get(ctx context.Context, org, id string) (*Node, error)
	GetByID(ctx context.Context, id string) (*Node, error)
	List(ctx context.Context, org string) ([]Node, error)
	ListJoinedByKey(ctx context.Context, org, keyID string) ([]Node, error)
	Update(ctx context.Context, n *Node) error
	// UpdateSettings writes only the settings u sets (nil leaves a column
	// alone) on a live node of org, and returns the stored node. A non-nil
	// Endpoints must be a subset of the stored set, else ErrWidenRemotely.
	UpdateSettings(ctx context.Context, org, id string, u SettingsUpdate) (*Node, error)
	// Touch writes last_seen_at and usage only, so a heartbeat appends no event.
	Touch(ctx context.Context, id string, at time.Time, usage nodeproto.Usage) error
	SetStatus(ctx context.Context, id string, s Status) error
	// ClaimSession raises the node's session epoch by one and returns it. The
	// epoch lives on the node, so it keeps rising across releases. It returns
	// ErrNotFound for a missing or revoked node.

	ClaimSession(ctx context.Context, s Session) (epoch int64, err error)
	Session(ctx context.Context, nodeID string) (*Session, error)
	// ReleaseSession deletes the session only when its epoch matches.
	ReleaseSession(ctx context.Context, nodeID string, epoch int64) error
	MarkStaleOffline(ctx context.Context, before time.Time) ([]string, error)
}
