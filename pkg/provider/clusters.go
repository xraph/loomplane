package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// AccountAccess carries verified authority for one provider call. Never persist it.
type AccountAccess struct {
	Provider     string `json:"-"`
	AccountID    string `json:"-"`
	ConnectionID string `json:"-"`
	Generation   int64  `json:"-"`
	Credential   []byte `json:"-"`
	// BeforeEffect must run after adapter preflight and immediately before purchase or credential issuance.
	BeforeEffect func(context.Context) error `json:"-"`
}

func (AccountAccess) String() string     { return "[account access]" }
func (a AccountAccess) GoString() string { return a.String() }
func (a AccountAccess) Format(s fmt.State, _ rune) {
	if _, err := io.WriteString(s, a.String()); err != nil {
		return
	}
}

// ClusterRef pins the physical cluster within its immutable account identity.
type ClusterRef struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Region      string `json:"region"`
	Incarnation string `json:"incarnation"`
}

// ClusterProviderOptions identifies the schema used by a provider's typed payload.
// Adapters must decode the named schema and reject unsupported fields or versions.
type ClusterProviderOptions struct {
	Provider string          `json:"provider"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
}

type ClusterMaintenance struct {
	Day       string `json:"day"`
	StartTime string `json:"start_time"`
}

// ClusterSpec is durable creation intent. ReconcileOnly forbids another create.
type ClusterSpec struct {
	OperationID     string                  `json:"operation_id"`
	Name            string                  `json:"name"`
	Region          string                  `json:"region"`
	Version         string                  `json:"version"`
	NetworkID       string                  `json:"network_id"`
	HA              bool                    `json:"ha"`
	NodePools       []NodePoolSpec          `json:"node_pools"`
	Maintenance     ClusterMaintenance      `json:"maintenance"`
	ProviderOptions *ClusterProviderOptions `json:"provider_options,omitempty"`
	ReconcileOnly   bool                    `json:"-"`
}

type NodePoolTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

type NodePoolSpec struct {
	Name            string                  `json:"name"`
	Size            string                  `json:"size"`
	Count           int                     `json:"count"`
	Min             int                     `json:"min"`
	Max             int                     `json:"max"`
	Autoscale       bool                    `json:"autoscale"`
	Labels          map[string]string       `json:"labels,omitempty"`
	Taints          []NodePoolTaint         `json:"taints,omitempty"`
	ProviderOptions *ClusterProviderOptions `json:"provider_options,omitempty"`
}

type ClusterCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ClusterObservation describes provider state, independently of runtime readiness.
type ClusterObservation struct {
	Name       string             `json:"name"`
	Ref        ClusterRef         `json:"ref"`
	Endpoint   string             `json:"endpoint"`
	CAHash     string             `json:"ca_hash"`
	Version    string             `json:"version"`
	State      string             `json:"state"`
	Pools      []NodePoolSpec     `json:"pools"`
	Conditions []ClusterCondition `json:"conditions"`
	ObservedAt time.Time          `json:"observed_at"`
}

// ClusterNodeSize carries live capacity facts for a provider-supported node size.
// A nil price means the provider did not supply one.
type ClusterNodeSize struct {
	ID         string   `json:"id"`
	CPUs       int      `json:"cpus"`
	MemoryMiB  int      `json:"memory_mib"`
	DiskGB     int      `json:"disk_gb"`
	HourlyUSD  *float64 `json:"hourly_usd,omitempty"`
	MonthlyUSD *float64 `json:"monthly_usd,omitempty"`
	Regions    []string `json:"regions"`
}

type ClusterOptions struct {
	AccountID            string                  `json:"account_id"`
	ConnectionGeneration int64                   `json:"connection_generation"`
	NodeSizeFacts        []ClusterNodeSize       `json:"node_size_facts"`
	Versions             []string                `json:"versions"`
	Regions              []string                `json:"regions"`
	NodeSizes            []string                `json:"node_sizes"`
	Networks             []string                `json:"networks"`
	Digest               string                  `json:"digest"`
	CheckedAt            time.Time               `json:"checked_at"`
	ProviderOptions      *ClusterProviderOptions `json:"provider_options,omitempty"`
}

// ClusterMutation may identify the durable request marker before a remote ID exists.
type ClusterMutation struct {
	OperationID   string     `json:"operation_id"`
	Ref           ClusterRef `json:"ref"`
	Kind          string     `json:"kind"`
	RequestDigest string     `json:"request_digest"`
}

// BootstrapAccess is transient management access. All material stays private.
type BootstrapAccess struct {
	Endpoint          string    `json:"-"`
	CAData            []byte    `json:"-"`
	Token             string    `json:"-"`
	ClientCertificate []byte    `json:"-"`
	ClientKey         []byte    `json:"-"`
	ExpiresAt         time.Time `json:"-"`
}

func (BootstrapAccess) String() string     { return "[cluster bootstrap access]" }
func (a BootstrapAccess) GoString() string { return a.String() }
func (a BootstrapAccess) Format(s fmt.State, _ rune) {
	if _, err := io.WriteString(s, a.String()); err != nil {
		return
	}
}

type ClusterPage struct {
	Items      []ClusterObservation `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type ClusterReader interface {
	Options(context.Context, AccountAccess, string) (ClusterOptions, error)
	ListClusters(context.Context, AccountAccess, string) (ClusterPage, error)
	GetCluster(context.Context, AccountAccess, ClusterRef) (ClusterObservation, error)
}

type ClusterCreator interface {
	CreateCluster(context.Context, AccountAccess, ClusterSpec) (ClusterMutation, error)
}

type ClusterObserver interface {
	ObserveClusterMutation(context.Context, AccountAccess, ClusterMutation) (ClusterObservation, error)
}

type ClusterBootstrap interface {
	BootstrapCluster(context.Context, AccountAccess, ClusterRef, time.Duration) (BootstrapAccess, error)
}

// ClusterRequestDigest binds recovery tags to the exact durable provider request.
func ClusterRequestDigest(spec ClusterSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ClusterSpecValidator checks provider-specific fields before purchase approval.
type ClusterSpecValidator interface{ ValidateClusterSpec(ClusterSpec) error }
