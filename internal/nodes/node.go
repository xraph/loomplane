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
)

// Node is one machine in an org.
type Node struct {
	ID                 string
	OrgID              string
	Name               string
	PublicKey          []byte
	KeyFingerprint     string
	IdentityGeneration int64
	Status             Status
	Endpoints          []nodeproto.Endpoint
	IngressMode        string // "none", "direct" or "relay"
	Role               string // "node" or "edge"
	EdgeScope          string // "", "org" or "instance"
	Labels             map[string]string
	Version            string
	ProtocolVersion    int
	Inventory          nodeproto.Inventory
	LastSeenAt         *time.Time
	JoinedBy           string
	JoinedByKeyID      string
	RevokedAt          *time.Time
	RevokedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Session is the live connection a replica holds for a node. A newer epoch
// fences an older connection.
type Session struct {
	NodeID      string
	Epoch       int64
	Replica     string
	RemoteAddr  string
	ConnectedAt time.Time
}

// Store persists nodes and their sessions.
type Store interface {
	Create(ctx context.Context, n *Node) error
	Get(ctx context.Context, org, id string) (*Node, error)
	GetByID(ctx context.Context, id string) (*Node, error)
	List(ctx context.Context, org string) ([]Node, error)
	ListJoinedByKey(ctx context.Context, org, keyID string) ([]Node, error)
	Update(ctx context.Context, n *Node) error
	// Touch writes last_seen_at and usage only, so a heartbeat appends no event.
	Touch(ctx context.Context, id string, at time.Time, usage nodeproto.Usage) error
	SetStatus(ctx context.Context, id string, s Status) error
	// ClaimSession sets the node's epoch to the old value plus one and returns it.
	ClaimSession(ctx context.Context, s Session) (epoch int64, err error)
	Session(ctx context.Context, nodeID string) (*Session, error)
	// ReleaseSession deletes the session only when its epoch matches.
	ReleaseSession(ctx context.Context, nodeID string, epoch int64) error
	MarkStaleOffline(ctx context.Context, before time.Time) ([]string, error)
}
