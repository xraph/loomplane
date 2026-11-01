package owned

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/loomplane/pkg/manifest"
)

const LifecycleCapabilityRevision = "kubernetes-lifecycle-job-v1"

var ErrLifecycleAuthority = errors.New("lifecycle job requires current instance authority")
var ErrLifecyclePending = errors.New("lifecycle job is awaiting an authoritative outcome")
var ErrLifecycleFailed = errors.New("lifecycle job failed")
var lifecyclePullSecret = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
var lifecycleName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// LifecycleJobRun contains the sealed release step and destination identity.
// Runtime values are resolved separately and never persisted in a receipt.
type LifecycleJobRun struct {
	OrgID, ProjectID, FleetID, InstanceID, EnvironmentID, OperationID, Name string
	Generation, OwnershipEpoch                                              int64
	Namespace, NamespaceUID, ConnectionID, ClusterCA, Architecture          string
	CapabilityRevision                                                      string
	// SchemaLockID is the server-resolved data resource and schema identity.
	SchemaLockID    string `json:",omitempty"`
	ImagePullSecret string `json:",omitempty"`
	Job             manifest.LifecycleJob
}

func (r LifecycleJobRun) ID() string {
	raw, _ := json.Marshal([]any{r.OrgID, r.FleetID, r.InstanceID, r.OperationID, r.Generation, r.Name}) //nolint:errcheck // the identity contains only strings and an integer
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func (r LifecycleJobRun) Digest() string {
	raw, _ := json.Marshal(r) //nolint:errcheck // the descriptor contains only JSON values
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
func (r LifecycleJobRun) Validate() error {
	for _, value := range []string{r.OrgID, r.ProjectID, r.FleetID, r.InstanceID, r.EnvironmentID, r.OperationID, r.Namespace, r.NamespaceUID, r.ConnectionID} {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			return ErrLifecycleAuthority
		}
	}
	if r.Generation < 1 || r.OwnershipEpoch < 1 || !lifecycleName.MatchString(r.Name) || r.CapabilityRevision != LifecycleCapabilityRevision || len(r.ClusterCA) != 64 || (r.Architecture != "arm64" && r.Architecture != "amd64") {
		return ErrLifecycleAuthority
	}
	if _, err := hex.DecodeString(r.ClusterCA); err != nil || r.ClusterCA != strings.ToLower(r.ClusterCA) {
		return ErrLifecycleAuthority
	}
	if r.ImagePullSecret != "" && (len(r.ImagePullSecret) > 253 || !lifecyclePullSecret.MatchString(r.ImagePullSecret)) {
		return ErrLifecycleAuthority
	}
	if (r.Job.SchemaLock != "") != (r.SchemaLockID != "") || len(r.SchemaLockID) > 512 || strings.ContainsAny(r.SchemaLockID, "\x00\r\n") {
		return ErrLifecycleAuthority
	}
	if r.Job.Architecture != "" && r.Job.Architecture != r.Architecture {
		return ErrLifecycleAuthority
	}
	return r.Job.Validate()
}
func (r LifecycleJobRun) AttemptName(attempt int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", r.ID(), attempt)))
	return "lp-lifecycle-" + hex.EncodeToString(sum[:20])
}

// LifecycleJobReceipt describes a provider-observed attempt. Pending never
// releases its schema lock, even after a worker crashes or loses a response.
type LifecycleJobReceipt struct {
	RunID, Digest, Name, NamespaceUID, JobUID, SecretUID string
	Attempt                                              int
	State                                                string
	Reason                                               string
}

func (r LifecycleJobReceipt) Terminal() bool { return r.State == "succeeded" || r.State == "failed" }

type LifecycleJobBackend interface {
	CreateLifecycleJob(context.Context, LifecycleJobRun, int) (*LifecycleJobReceipt, error)
	ObserveLifecycleJob(context.Context, LifecycleJobRun, int) (*LifecycleJobReceipt, error)
	CleanupLifecycleJob(context.Context, LifecycleJobRun, LifecycleJobReceipt) error
}
