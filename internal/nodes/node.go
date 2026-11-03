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
	// ErrKeyKnown is returned when a joining key already belongs to a node,
	// live or revoked. A key never gets a second node.
	ErrKeyKnown = errors.New("nodes: key already belongs to a node")
	// ErrWidenRemotely is returned when an update would add endpoints. Only
	// the machine's owner can widen what it serves.
	ErrWidenRemotely = errors.New("add endpoints on the machine with: loomctl node allow <endpoint>")
	// ErrNonceUsed is returned when a challenge nonce has been spent already.
	ErrNonceUsed = errors.New("nodes: challenge nonce already used")
)

// Node is one machine in an org.
type Node struct {
	ID                 string               `json:"id" forge:"id"`
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

	// PublicAddress is the address Loomplane fetched a nonce from, through
	// the node's own proxy, at PublicAddressConfirmedAt. Routes reach the
	// node there only once it is confirmed.
	PublicAddress            string     `json:"public_address"`
	PublicAddressConfirmedAt *time.Time `json:"public_address_confirmed_at"`
	// IngressApplied is the mode the node last said it runs, and
	// IngressSentAt when the stored mode was last sent to it.
	IngressApplied string     `json:"ingress_applied"`
	IngressSentAt  *time.Time `json:"-"`
	// IngressDetail says why ingress isn't what was asked: the last address
	// check's finding, or, worked out on read, a mode the node didn't apply.
	IngressDetail string `json:"ingress_detail"`
}

// SettingsUpdate is the part of a node an owner may change remotely. A nil
// field is left alone; a non-nil empty slice or map is a real change.
type SettingsUpdate struct {
	Name        *string
	Labels      map[string]string
	IngressMode *string
	Endpoints   []nodeproto.Endpoint
}

// IngressSetting is what an address check settles: the mode, the confirmed
// address and when, and the detail an admin reads.
type IngressSetting struct {
	Mode          string
	PublicAddress string
	ConfirmedAt   *time.Time
	Detail        string
	// SentAt is when the mode goes out to the node.
	SentAt time.Time
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
	// Create inserts n. It returns ErrNameTaken when a live node of the org
	// has the name, and ErrKeyKnown when any node, revoked ones included,
	// has the key.
	Create(ctx context.Context, n *Node) error
	// KeyKnown reports whether any node, revoked ones included, has the key
	// with this fingerprint.
	KeyKnown(ctx context.Context, fingerprint string) (bool, error)
	// NameTaken reports whether a live node of org has the name.
	NameTaken(ctx context.Context, org, name string) (bool, error)
	Get(ctx context.Context, org, id string) (*Node, error)
	GetByID(ctx context.Context, id string) (*Node, error)
	List(ctx context.Context, org string) ([]Node, error)
	ListJoinedByKey(ctx context.Context, org, keyID string) ([]Node, error)
	Update(ctx context.Context, n *Node) error
	// UpdateSettings writes only the settings u sets (nil leaves a column
	// alone) on a live node of org, and returns the stored node. A non-nil
	// Endpoints must be a subset of the stored set, else ErrWidenRemotely.
	UpdateSettings(ctx context.Context, org, id string, u SettingsUpdate) (*Node, error)
	// SetIngress writes in on a node that is not revoked, and returns the
	// stored node.
	SetIngress(ctx context.Context, id string, in IngressSetting) (*Node, error)
	// RecordIngressApplied writes the mode the node says it now runs.
	RecordIngressApplied(ctx context.Context, id, mode string) error
	// SetIngressDetail writes only the detail an admin reads on a node that
	// is not revoked, such as an address check's finding.
	SetIngressDetail(ctx context.Context, id, detail string) error
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
	// MarkOfflineIfEpoch sets a live node offline only while epoch is still
	// its current session epoch, so an ending session can't clobber the
	// status of a newer one. No match is not an error.
	MarkOfflineIfEpoch(ctx context.Context, id string, epoch int64) error
	// ApplyInventory writes a node's reported inventory, version and protocol
	// version on a node that is not revoked. It never touches endpoints or
	// status: Inventory.Endpoints is informational.
	ApplyInventory(ctx context.Context, id string, inv nodeproto.Inventory, version string, protocolVersion int) error
	// AllowEndpoints adds endpoints the machine's owner allowed on the machine
	// to a node that is not revoked, and returns the stored node.
	AllowEndpoints(ctx context.Context, id string, add []nodeproto.Endpoint) (*Node, error)
	// UseNonce spends a challenge nonce. A second use gives ErrNonceUsed.
	UseNonce(ctx context.Context, nonce string, expires time.Time) error
	// DeleteExpiredNonces drops spent nonces that expired before now.
	DeleteExpiredNonces(ctx context.Context, now time.Time) error
}
