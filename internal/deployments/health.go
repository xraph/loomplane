package deployments

import (
	"context"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/usage"
)

// RevisionMeter reads one revision's traffic from the cloud that runs it.
// An empty revision is the whole service. Implemented by *ECS, *CloudRun,
// *Azure and *Render.
type RevisionMeter interface {
	MeasureRevision(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding, revision string, w metrics.Window) (*metrics.Measurement, error)
}

// HealthReader turns a binding and a revision into releasehealth.Health, from
// each cloud's own monitoring.
type HealthReader struct {
	Bindings interface {
		GetBinding(ctx context.Context, orgID, id string) (*planning.Binding, error)
	}
	Credentials Credentials
	Meters      map[string]RevisionMeter // by provider
}

var _ releasehealth.Reader = (*HealthReader)(nil)

// Health implements releasehealth.Reader. A cloud that can't say, or has no
// meter, gives Available false with the reason; any other failure is an error.
func (h *HealthReader) Health(ctx context.Context, orgID, bindingID, revision string, w releasehealth.Window) (releasehealth.Health, error) {
	b, err := h.Bindings.GetBinding(ctx, orgID, bindingID)
	if err != nil {
		return releasehealth.Health{}, fmt.Errorf("read the binding to judge: %w", err)
	}
	meter := h.Meters[b.Provider]
	if meter == nil {
		reason := usage.NoTraffic[b.Provider]
		if reason == "" {
			reason = b.Provider + " doesn't report requests or latency through an API Loomplane reads."
		}
		return releasehealth.Health{Reason: reason}, nil
	}
	mw, err := metrics.ShortWindow(w.Start, w.End)
	if err != nil {
		return releasehealth.Health{}, err
	}
	conn, credential, err := h.Credentials.Credential(ctx, orgID, b.ConnectionID)
	if errors.Is(err, connections.ErrNotFound) {
		return releasehealth.Health{Reason: fmt.Sprintf("The connection that deployed %s is gone.", b.Name)}, nil
	}
	if err != nil {
		return releasehealth.Health{}, fmt.Errorf("read the credential to judge: %w", err)
	}
	m, err := meter.MeasureRevision(ctx, conn, credential, b, revision, mw)
	if reason, ok := metrics.IsUnavailable(err); ok {
		return releasehealth.Health{Reason: reason}, nil
	}
	if err != nil {
		return releasehealth.Health{}, err
	}
	if m == nil {
		return releasehealth.Health{Reason: "The cloud returned no measurement."}, nil
	}
	if !m.ErrorsCounted {
		// Zero errors and errors nobody counted look the same in a sum.
		return releasehealth.Health{Reason: "The cloud didn't report 5xx error counts, so the error rate can't be judged."}, nil
	}
	return releasehealth.Health{
		Requests:  metrics.Total(metrics.Grid(mw, m.Requests)),
		Errors:    metrics.Total(metrics.Grid(mw, m.Errors)),
		LatencyMS: m.Latency,
		Statistic: m.Statistic,
		Available: true,
	}, nil
}
