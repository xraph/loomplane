// Package platformcloud holds the control plane for the platform Cloud:
// regions, the Firecracker hosts in them, the organizations allowed to use
// them and where each workload is placed.
package platformcloud

import (
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/instance"
)

var (
	ErrNotFound   = errors.New("platformcloud: not found")
	ErrConflict   = errors.New("platformcloud: conflict")
	ErrNotEnabled = errors.New("platformcloud: platform cloud is not enabled for this organization")
	ErrSuspended  = errors.New("platformcloud: platform cloud is suspended for this organization")
	ErrLimit      = errors.New("platformcloud: limit reached")
	ErrNoCapacity = errors.New("platformcloud: no host has capacity")
)

// Limits caps what one organization may use. Use reports what it does use.
type Limits = instance.PlatformLimits

// Use is what an organization's live placements add up to.
type Use struct {
	VCPUs            int `json:"vcpus"`
	MemoryMiB        int `json:"memory_mib"`
	VMs              int `json:"vms"`
	ConcurrentBuilds int `json:"concurrent_builds"`
}

type Region struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
}

// Evidence is what a host reported about itself at its last check.
type Evidence struct {
	Isolation       bool      `json:"isolation"`
	Platform        bool      `json:"platform"`
	SMTActive       bool      `json:"smt_active"`
	RuntimeIdentity string    `json:"runtime_identity"`
	Architecture    string    `json:"architecture"`
	ProtocolVersion string    `json:"protocol_version"`
	DeniedPorts     []int     `json:"denied_ports"`
	PerOrgVMs       int       `json:"per_org_vms"`
	PerOrgVCPUs     int       `json:"per_org_vcpus"`
	PerOrgMemoryMiB int       `json:"per_org_memory_mib"`
	IngressAddress  string    `json:"ingress_address"`
	CheckedAt       time.Time `json:"checked_at"`
	// Reason says why the last check left the host pending. It is empty
	// when the check passed.
	Reason string `json:"reason,omitempty"`
}

// Allotment is capacity: what a host offers, or what is placed on it.
type Allotment struct {
	VCPUs     int `json:"vcpus"`
	MemoryMiB int `json:"memory_mib"`
	DiskMiB   int `json:"disk_mib"`
	VMs       int `json:"vms"`
}

type Host struct {
	ID                string    `json:"id"`
	RegionID          string    `json:"region_id"`
	Endpoint          string    `json:"endpoint"`
	HostID            string    `json:"host_id"`
	Status            string    `json:"status"`
	ClientFingerprint string    `json:"client_fingerprint"`
	Evidence          Evidence  `json:"evidence"`
	Allotment         Allotment `json:"allotment"`
	Generation        int64     `json:"generation"`
	Revision          int64     `json:"revision"`
}

// SuspendReason says why an organization's platform workloads are suspended.
type SuspendReason string

const (
	ReasonSpendingLimit SuspendReason = "spending_limit"
	ReasonPastDue       SuspendReason = "past_due"
	ReasonPlatformOwner SuspendReason = "platform_owner"
)

type Tenant struct {
	OrgID          string          `json:"org_id"`
	Status         string          `json:"status"`
	Limits         *Limits         `json:"limits,omitempty"`
	SuspendReasons []SuspendReason `json:"suspend_reasons"`
	SuspendedAt    *time.Time      `json:"suspended_at,omitempty"`
	Revision       int64           `json:"revision"`
}

type Placement struct {
	ID              string     `json:"id"`
	OrgID           string     `json:"org_id"`
	ProjectID       string     `json:"project_id"`
	EnvironmentID   string     `json:"environment_id"`
	Resource        string     `json:"resource"`
	Kind            string     `json:"kind"`
	RegionID        string     `json:"region_id"`
	HostID          *string    `json:"host_id,omitempty"`
	VCPUs           int        `json:"vcpus"`
	MemoryMiB       int        `json:"memory_mib"`
	DiskMiB         int        `json:"disk_mib"`
	State           string     `json:"state"`
	RestoreRunning  bool       `json:"restore_running"`
	WaitingCapacity bool       `json:"waiting_capacity"`
	InstanceID      string     `json:"instance_id"`
	Generation      int64      `json:"generation"`
	SuspendedAt     *time.Time `json:"suspended_at,omitempty"`
}
