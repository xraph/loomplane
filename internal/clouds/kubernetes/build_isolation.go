package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/xraph/loomplane/internal/buildpolicy"
)

func buildIsolationSettings(settings, details map[string]string) error {
	workers := strings.TrimSpace(settings["build_workers"])
	if err := buildpolicy.ValidateWorkers(workers); err != nil {
		return err
	}
	if workers != "" {
		details["build_workers"] = workers
	}
	class := strings.TrimSpace(settings["build_runtime_class"])
	if class != "" {
		if len(validation.IsDNS1123Subdomain(class)) != 0 {
			return errors.New("build_runtime_class must be a Kubernetes RuntimeClass name")
		}
		details["build_runtime_class"] = class
	}
	return nil
}

// runtimeIdentity pins the administrator's runtime mapping, scheduling and
// overhead. The node administrator remains responsible for installing Kata.
func (c *Client) runtimeIdentity(ctx context.Context) (string, error) {
	class, err := c.Kube.NodeV1().RuntimeClasses().Get(ctx, c.Config.Details["build_runtime_class"], metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read the Kata build RuntimeClass: %w", apiReason(err))
	}
	if class.UID == "" || class.DeletionTimestamp != nil || (class.Handler != "kata" && !strings.HasPrefix(class.Handler, "kata-")) {
		return "", errors.New("the build RuntimeClass must have a stable identity and a kata or kata-* handler")
	}
	raw, err := json.Marshal(struct {
		UID        string
		Handler    string
		Scheduling any
		Overhead   any
	}{string(class.UID), class.Handler, class.Scheduling, class.Overhead})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (c *Client) checkBuildIsolation(ctx context.Context, out *Result) {
	delete(out.Details, "build_isolation")
	delete(out.Details, "build_isolation_error")
	delete(out.Details, "build_runtime_identity")
	if c.Config.Details["build_runtime_class"] == "" {
		return
	}
	identity, err := c.runtimeIdentity(ctx)
	if err != nil {
		out.Details["build_isolation"] = "unavailable"
		out.Details["build_isolation_error"] = err.Error()
		return
	}
	out.Details["build_isolation"], out.Details["build_runtime_identity"] = "ready", identity
}

func (c *Client) verifyBuildIsolation(ctx context.Context) error {
	if err := buildpolicy.Check(c.Config.Details, true, false); err != nil {
		return err
	}
	if c.Config.Details["build_runtime_class"] == "" {
		return nil
	}
	identity, err := c.runtimeIdentity(ctx)
	if err != nil {
		return err
	}
	if identity != c.Config.Details["build_runtime_identity"] {
		return errors.New("the Kata build RuntimeClass changed; check the connection and plan again")
	}
	return nil
}
