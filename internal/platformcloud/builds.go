package platformcloud

import (
	"context"
	"log/slog"
	"time"

	"github.com/xraph/loomplane/internal/buildpolicy"
	fc "github.com/xraph/loomplane/internal/firecracker"
)

// Build VMs are the same size on every platform host.
const (
	BuildVCPUs     = 2
	BuildMemoryMiB = 2048
	BuildDiskMiB   = 16384
)

// errBuildHostPolicy is a build host that no longer passes the platform
// policy. Its text never names the host.
var errBuildHostPolicy = &workspaceError{msg: "Loomplane Cloud's build host failed its isolation check, so no source was sent to it. Try the build again."}

// CheckBuildHost refuses a host whose fresh capabilities don't pass the
// platform policy. A build calls it on a check made right before it sends the
// host any source. The reason goes to the platform owner's log; the error is
// safe to show the workspace.
func CheckBuildHost(ctx context.Context, orgID string, caps fc.Capabilities) error {
	if err := buildpolicy.CheckPlatform(platformEvidence(caps)); err != nil {
		slog.ErrorContext(ctx, "platform build host refused", "org", orgID, "error", err)
		return errBuildHostPolicy
	}
	return nil
}

// RecordBuild meters a build VM that ran from started to ended. Metering
// comes later; until then it records nothing.
func (s *Service) RecordBuild(_ context.Context, _ Placement, _, _ time.Time) error {
	return nil
}
