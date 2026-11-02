package cloudinfra

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	kube "github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterProblem struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Retryable  bool     `json:"retryable"`
	Resource   string   `json:"resource,omitempty"`
	Categories []string `json:"categories,omitempty"`
}

func clusterBlockResult(c forge.Context, err error) (bool, error) {
	status := http.StatusUnprocessableEntity
	var price *infra.ClusterPriceError
	var maintenance *infra.ClusterMaintenanceAdmissionError
	var lifecycle *provider.ClusterLifecycleBlockedError
	var problem ClusterProblem
	switch {
	case errors.As(err, &price):
		problem = ClusterProblem{Code: price.Reason, Message: "Required prices are unavailable. Refresh price data and review again when every required category is known.", Categories: price.Categories, Retryable: price.Reason == "stale_price"}
	case infra.ClusterAggregateUnsupported(err):
		status = http.StatusConflict
		problem = ClusterProblem{Code: "unsupported_capacity_schema", Message: "Stored capacity data needs reconciliation before you can review new work."}
	case errors.Is(err, infra.ErrClusterCapacityUnavailable):
		status = http.StatusConflict
		problem = ClusterProblem{Code: "capacity_unavailable", Message: "Refresh capacity with a current cluster management grant.", Retryable: true}
	case errors.Is(err, kube.ErrCleanupPermissionsUnavailable):
		problem = ClusterProblem{Code: "runtime_access_upgrade_required", Message: "Review a runtime access upgrade with a current cluster management grant before retrying."}
	case errors.Is(err, infra.ErrRuntimeCredentialDelegationUnavailable):
		problem = ClusterProblem{Code: "runtime_access_unavailable", Message: "The provider cannot delegate the reviewed runtime access. This capability needs a supported access upgrade."}
	case errors.As(err, &maintenance):
		problem = ClusterProblem{Code: maintenance.Reason, Message: "Cluster maintenance is blocked. Resolve the reported condition and create a fresh review."}
	case errors.As(err, &lifecycle):
		problem = ClusterProblem{Code: lifecycle.Reason, Message: "This lifecycle action is unavailable until its reported prerequisite is resolved."}
	default:
		return false, nil
	}
	return true, c.JSON(status, problem)
}
