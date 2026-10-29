package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// InfrastructureCapability describes an account API independently of its runtime.
type InfrastructureCapability string

// Infrastructure operations are declared independently. A declaration does not
// imply an implementation; registries advertise only installed adapter methods.
const (
	ClusterDiscovery           InfrastructureCapability = "cluster:discover"
	ClusterOptionsRead         InfrastructureCapability = "cluster:options"
	ClusterCreation            InfrastructureCapability = "cluster:create"
	ClusterMutationObservation InfrastructureCapability = "cluster:observe"
	ClusterBootstrapAccess     InfrastructureCapability = "cluster:bootstrap"
	ClusterConfigurationUpdate InfrastructureCapability = "cluster:update"
	ClusterUpgrade             InfrastructureCapability = "cluster:upgrade"
	ClusterDeletion            InfrastructureCapability = "cluster:delete"
	NodePoolCreation           InfrastructureCapability = "node-pool:create"
	NodePoolUpdate             InfrastructureCapability = "node-pool:update"
	NodePoolDeletion           InfrastructureCapability = "node-pool:delete"
	ClusterCapacityObservation InfrastructureCapability = "cluster:capacity:observe"
	ServerDiscovery            InfrastructureCapability = "server:discover"
	ServerAdoption             InfrastructureCapability = "server:adopt"
	ServerProvisioning         InfrastructureCapability = "server:provision"
	ServerResize               InfrastructureCapability = "server:resize"
	ServerDeletion             InfrastructureCapability = "server:delete"
	DNSZoneDiscovery           InfrastructureCapability = "dns:zones:read"
	DNSZoneCreation            InfrastructureCapability = "dns:zones:create"
	DNSRecordDiscovery         InfrastructureCapability = "dns:records:read"
	DNSRecordUpsert            InfrastructureCapability = "dns:records:upsert"
	DNSRecordDeletion          InfrastructureCapability = "dns:records:delete"
	ManagedDomainAttachment    InfrastructureCapability = "managed-domain:attach"
	IngressManagement          InfrastructureCapability = "ingress"
	TLSManagement              InfrastructureCapability = "tls"
	// DNSManagement identifies the lookup family when no DNS adapter is installed.
	DNSManagement InfrastructureCapability = "dns"
)

// InfrastructureCredential is resolved from Vault for one operation. AccountID
// is the verified immutable account or team ID, never its display name.
type InfrastructureCredential struct {
	Provider  string `json:"-"`
	AccountID string `json:"-"`
	TeamID    string `json:"-"`
	Token     string `json:"-"`
}

func (InfrastructureCredential) String() string { return "[infrastructure credential]" }

// GoString keeps secrets out of diagnostic formatting too.
func (c InfrastructureCredential) GoString() string { return c.String() }

// Infrastructure errors support errors.Is without including provider response bodies.
var (
	ErrInfrastructureInvalid     = errors.New("invalid infrastructure request")
	ErrInfrastructureUnsupported = errors.New("unsupported infrastructure capability")
	ErrInfrastructureScope       = errors.New("infrastructure permission denied")
	ErrInfrastructureIdentity    = errors.New("infrastructure account identity mismatch")
	ErrInfrastructureNotFound    = errors.New("infrastructure resource not found")
	ErrInfrastructureDrift       = errors.New("infrastructure resource ownership or value changed")
	ErrInfrastructureAmbiguous   = errors.New("infrastructure mutation outcome requires reconciliation")
	ErrInfrastructurePagination  = errors.New("infrastructure pagination limit or repeated cursor")
)

// UnsupportedCapabilityError names the provider and missing implementation.
type UnsupportedCapabilityError struct {
	Provider   string
	Capability InfrastructureCapability
}

func (e *UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("%s does not implement %s: %v", e.Provider, e.Capability, ErrInfrastructureUnsupported)
}
func (*UnsupportedCapabilityError) Unwrap() error { return ErrInfrastructureUnsupported }

// InfrastructureScopeError identifies the required permission without credentials.
type InfrastructureScopeError struct{ Provider, Permission string }

func (e *InfrastructureScopeError) Error() string {
	return fmt.Sprintf("%s requires %s: %v", e.Provider, e.Permission, ErrInfrastructureScope)
}
func (*InfrastructureScopeError) Unwrap() error { return ErrInfrastructureScope }

// InfrastructureAPIError is safe to persist. Raw remote bodies are excluded.
// Codes are the provider's numeric error codes, never its messages.
type InfrastructureAPIError struct {
	RetryAfter time.Duration
	Provider   string
	Status     int
	Codes      []int
}

func (e *InfrastructureAPIError) Error() string {
	return fmt.Sprintf("%s infrastructure API answered %d", e.Provider, e.Status)
}

// ServerSpec is one durable creation intent. Persist the intent and an attempted
// marker before calling CreateServer. Every retry after that call starts must
// use ReconcileOnly. Absence from a listing cannot prove a create did not run.
// UserData is transient bootstrap material and must never enter stored intent JSON.
type ServerSpec struct {
	OperationID   string   `json:"operation_id"`
	Name          string   `json:"name"`
	Region        string   `json:"region"`
	Size          string   `json:"size"`
	Image         string   `json:"image"`
	SSHKeys       []string `json:"ssh_keys,omitempty"`
	UserData      string   `json:"-"`
	ReconcileOnly bool     `json:"-"`
}

func (s ServerSpec) String() string { return "[server creation intent]" }

// GoString excludes bootstrap secrets from diagnostic formatting.
func (s ServerSpec) GoString() string { return s.String() }

// InfrastructureServer contains provider facts, not runtime readiness. ID and
// AccountID must be retained even if the host is renamed or its token rotates.
type InfrastructureServer struct {
	ID         string   `json:"id"`
	AccountID  string   `json:"account_id"`
	Name       string   `json:"name"`
	Region     string   `json:"region"`
	Size       string   `json:"size"`
	Status     string   `json:"status"`
	PublicIPv4 []string `json:"public_ipv4"`
	PublicIPv6 []string `json:"public_ipv6"`
	Tags       []string `json:"tags"`
}

// InfrastructureAction is an asynchronous provider operation. Success of the
// HTTP request means accepted. Only Status completed means the action finished.
type InfrastructureAction struct {
	ID         string `json:"id"`
	ResourceID string `json:"resource_id"`
	Status     string `json:"status"`
	Kind       string `json:"kind"`
}

// ServerResult preserves an accepted resource and its pending action IDs.
type ServerResult struct {
	Server     InfrastructureServer   `json:"server"`
	Actions    []InfrastructureAction `json:"actions,omitempty"`
	Reconciled bool                   `json:"reconciled"`
}

// ServerAdapter provisions hosts. The existing SSH adapter owns their runtime.
type ServerAdapter interface {
	ListServers(context.Context, InfrastructureCredential) ([]InfrastructureServer, error)
	GetServer(context.Context, InfrastructureCredential, string) (InfrastructureServer, error)
	CreateServer(context.Context, InfrastructureCredential, ServerSpec) (ServerResult, error)
	ResizeServer(context.Context, InfrastructureCredential, string, string) (InfrastructureAction, error)
	DeleteServer(context.Context, InfrastructureCredential, string) error
	GetServerAction(context.Context, InfrastructureCredential, string, string) (InfrastructureAction, error)
}

// DNSZone identifies a zone in its account. Name is also the request locator on
// APIs that address domains by name. Nameservers are read-only metadata here.
// Verified describes provider hosting metadata, not public control of the name.
type DNSZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	AccountID   string   `json:"account_id"`
	Nameservers []string `json:"nameservers,omitempty"`
	Verified    bool     `json:"verified"`
}

// DNSZoneSpec adds DNS hosting for an existing registered domain. It never buys,
// transfers, changes registrar nameservers or assigns the domain to a project.
type DNSZoneSpec struct {
	Name          string `json:"name"`
	ReconcileOnly bool   `json:"-"`
}

// DNSRecord is the complete ownership receipt callers must persist. Owner is a
// provider comment where supported; DigitalOcean ownership uses the recorded ID.
// ID is the provider's current record version identity. A provider may rotate it
// on update, so persist the returned receipt before further mutation or cleanup.
// An empty Name means the zone apex, and names are relative to the zone.
type DNSRecord struct {
	ID     string `json:"id"`
	ZoneID string `json:"zone_id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
	Owner  string `json:"owner,omitempty"`
}

// DNSUpsertRequest creates only if Expected is nil and the name is unclaimed.
// Updates supply the prior ID, name and type unchanged; the returned provider ID
// may rotate. Expected must be the exact persisted prior receipt.
// ReconcileOnly forbids a new create. For Vercel updates it performs read-only
// reconciliation using the persisted owner and complete intended state, including
// a provider-rotated ID, and returns ambiguity while that state is unresolved.
type DNSUpsertRequest struct {
	Zone          DNSZone    `json:"zone"`
	Record        DNSRecord  `json:"record"`
	Expected      *DNSRecord `json:"expected,omitempty"`
	ReconcileOnly bool       `json:"-"`
}

// DNSDeleteRequest deletes only the exact recorded prior resource.
type DNSDeleteRequest struct {
	Zone     DNSZone   `json:"zone"`
	Expected DNSRecord `json:"expected"`
}

// DNSAdapter manages DNS independently of compute. Expected-prior checks are
// drift guards, not atomic compare-and-swap, because these APIs provide no CAS.
// Callers must serialize changes by zone/name and reconcile ambiguous outcomes.
// DNSRecordOwnership is an optional capability for adapters that persist an
// owner token on provider records. Other adapters require recorded IDs.
type DNSRecordOwnership interface{ SupportsDNSRecordOwnership() bool }

type DNSAdapter interface {
	ListZones(context.Context, InfrastructureCredential) ([]DNSZone, error)
	GetZone(context.Context, InfrastructureCredential, string) (DNSZone, error)
	CreateZone(context.Context, InfrastructureCredential, DNSZoneSpec) (DNSZone, error)
	ListRecords(context.Context, InfrastructureCredential, DNSZone) ([]DNSRecord, error)
	UpsertRecord(context.Context, InfrastructureCredential, DNSUpsertRequest) (DNSRecord, error)
	DeleteRecord(context.Context, InfrastructureCredential, DNSDeleteRequest) error
}

var dnsLabelRE = regexp.MustCompile(`^[a-z0-9_*-]+$`)

// ValidateDNSName accepts canonical relative ASCII labels, excluding URL syntax.
func ValidateDNSName(name string, apex bool) error {
	if name == "" && apex {
		return nil
	}
	if len(name) > 253 || name == "" || name != strings.ToLower(name) {
		return ErrInfrastructureInvalid
	}
	for index, label := range strings.Split(name, ".") {
		if (!apex && strings.ContainsAny(label, "_*")) || (strings.Contains(label, "*") && index != 0) || label == "" || len(label) > 63 || !dnsLabelRE.MatchString(label) || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") || (strings.Contains(label, "*") && label != "*") {
			return ErrInfrastructureInvalid
		}
	}
	return nil
}

// ValidateDNSRecord restricts mutations to the record types this contract models.
func ValidateDNSRecord(r DNSRecord) error {
	if err := ValidateDNSName(r.Name, true); err != nil {
		return err
	}
	if r.TTL < 60 || r.TTL > 2147483647 || r.Value == "" || len(r.Value) > 4096 || len(r.Owner) > 500 {
		return ErrInfrastructureInvalid
	}
	switch r.Type {
	case "A", "AAAA":
		addr, err := netip.ParseAddr(r.Value)
		if err != nil || (r.Type == "A" && !addr.Is4()) || (r.Type == "AAAA" && (!addr.Is6() || addr.Is4In6())) {
			return ErrInfrastructureInvalid
		}
	case "CNAME":
		return ValidateDNSName(strings.TrimSuffix(r.Value, "."), false)
	case "TXT":
	default:
		return ErrInfrastructureUnsupported
	}
	return nil
}

// MatchDNSPrior refuses a changed or foreign resource. The service must obtain
// expected from its durable ownership record, never from the current listing.
func MatchDNSPrior(expected, current DNSRecord) error {
	if expected.ID == "" || expected.ZoneID == "" || expected != current {
		return ErrInfrastructureDrift
	}
	return nil
}

// ServerOperationTag is a stable, provider-safe reconciliation key.
func ServerOperationTag(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return "loomplane-op-" + hex.EncodeToString(sum[:])
}

// PlanDNSUpsert checks a complete zone snapshot before mutation. Its boolean
// reports whether the requested state is already present. With no provider
// owner comments, an ambiguous create cannot be adopted from equal values.
func PlanDNSUpsert(req DNSUpsertRequest, records []DNSRecord, ownerComments bool) (DNSRecord, bool, error) {
	if req.Zone.ID == "" || req.Zone.Name == "" || req.Record.ZoneID != req.Zone.ID {
		return DNSRecord{}, false, ErrInfrastructureInvalid
	}
	if err := ValidateDNSRecord(req.Record); err != nil {
		return DNSRecord{}, false, err
	}
	if req.Expected != nil {
		prior := *req.Expected
		if ownerComments && !strings.HasPrefix(prior.Owner, "loomplane:") {
			return DNSRecord{}, false, ErrInfrastructureDrift
		}
		if req.Record.ID != prior.ID || req.Record.ZoneID != prior.ZoneID || req.Record.Name != prior.Name || req.Record.Type != prior.Type || req.Record.Owner != prior.Owner {
			return DNSRecord{}, false, ErrInfrastructureDrift
		}
		for _, r := range records {
			if r.ID == prior.ID {
				if err := MatchDNSPrior(prior, r); err != nil {
					return DNSRecord{}, false, err
				}
				return r, r == req.Record, nil
			}
		}
		return DNSRecord{}, false, ErrInfrastructureDrift
	}
	if req.Record.ID != "" {
		return DNSRecord{}, false, ErrInfrastructureInvalid
	}
	if ownerComments && (req.Record.Owner == "" || !strings.HasPrefix(req.Record.Owner, "loomplane:")) {
		return DNSRecord{}, false, ErrInfrastructureInvalid
	}
	if !ownerComments && req.Record.Owner != "" {
		return DNSRecord{}, false, ErrInfrastructureInvalid
	}
	var recovered DNSRecord
	for _, r := range records {
		if r.Name != req.Record.Name {
			continue
		}
		if r.Type != req.Record.Type && r.Type != "CNAME" && req.Record.Type != "CNAME" {
			continue
		}
		desired := req.Record
		desired.ID = r.ID
		if !ownerComments || r != desired || recovered.ID != "" {
			return DNSRecord{}, false, ErrInfrastructureDrift
		}
		recovered = r
	}
	if recovered.ID != "" {
		return recovered, true, nil
	}
	if req.ReconcileOnly {
		return DNSRecord{}, false, ErrInfrastructureAmbiguous
	}
	return DNSRecord{}, false, nil
}
