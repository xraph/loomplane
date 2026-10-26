// Package usage reports what an organization's clouds say about it:
// what each account has cost this month, and the requests and latency of
// every service Loomplane runs. Loomplane never carries application
// traffic, so every number is read from the cloud's own billing and
// monitoring, with the credential the organization connected.
//
// A cloud that can't report something says why, so the dashboard can name
// the permission to grant rather than show a blank.
package usage

import (
	"context"
	"errors"
	"time"

	"github.com/xraph/loomplane/internal/clouds/metrics"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/telemetry"
)

// ErrInvalid is a request the service can't answer as asked, such as an
// unknown range.
var ErrInvalid = errors.New("invalid usage request")

// Connections reads an organization's cloud connections and reveals a
// ready one's credential. *connections.Service is the real one; neither
// method checks a role, so the service authorizes first.
type Connections interface {
	All(ctx context.Context, orgID string) ([]*connections.Connection, error)
	Credential(ctx context.Context, orgID, id string) (*connections.Connection, []byte, error)
}

// Bindings lists the services an organization runs. *planning.PgStore is
// the real one.
type Bindings interface {
	OrgServiceBindings(ctx context.Context, orgID string) ([]*planning.Binding, error)
}

// Authorizer decides whether a principal may act.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// Meter reads a service's requests and latency over a window from the
// provider that runs it. The deployment executors that can are Meters.
type Meter interface {
	Measure(ctx context.Context, conn *connections.Connection, credential []byte, b *planning.Binding, w metrics.Window) (*metrics.Measurement, error)
}

// SpendReader reads what a connected account has cost this month.
type SpendReader interface {
	Spend(ctx context.Context, conn *connections.Connection, credential []byte, now time.Time) (*metrics.Spend, error)
}

// Deps are what a Service reads with.
type Deps struct {
	Connections Connections
	Bindings    Bindings
	Authz       Authorizer
	// Meters are by provider, such as cloud-run.
	Meters map[string]Meter
	// Spenders are by cloud, such as gcp.
	Spenders map[string]SpendReader
	// Title names a provider or cloud for people. It may be nil.
	Title func(string) string
	// Warn logs a cloud that failed to answer. It may be nil.
	Warn func(msg string, err error)
	// Telemetry counts reads by outcome. It may be nil.
	Telemetry *telemetry.Recorder
}

// Service answers the usage API.
type Service struct {
	Deps
	now   func() time.Time
	cache *cache
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Title == nil {
		d.Title = func(s string) string { return s }
	}
	if d.Warn == nil {
		d.Warn = func(string, error) {}
	}
	s := &Service{Deps: d, now: time.Now}
	s.cache = newCache(func() time.Time { return s.now() })
	return s
}

// Statuses of a source.
const (
	// Reporting is a source that answered.
	Reporting = "reporting"
	// Unavailable is a source that can't answer until someone acts, such
	// as granting a permission. Reason says what.
	Unavailable = "unavailable"
	// Failed is a source that didn't answer this time.
	Failed = "failed"
	// Unsupported is a source whose cloud has no API for this, so there's
	// nothing to fix. Reason says so.
	Unsupported = "unsupported"
)

// unsupportedError is a cloud that has no API for what was asked.
type unsupportedError struct{ reason string }

func (u *unsupportedError) Error() string { return u.reason }

// Source is a connection the numbers came from, or would have.
type Source struct {
	ConnectionID string `json:"connection_id"`
	Cloud        string `json:"cloud"`
	// Name is the connection's name.
	Name   string `json:"name"`
	Status string `json:"status"`
	// Reason says what to do about a source that isn't reporting.
	Reason string `json:"reason,omitempty"`
	// Services are the services this applies to, for traffic.
	Services []string `json:"services,omitempty"`
}

// How long answers are kept. Bills change a few times a day and some
// clouds charge for each read; requests change by the minute. Failures
// are kept briefly, so a fixed permission shows up soon.
const (
	spendTTL   = time.Hour
	trafficTTL = 5 * time.Minute
	failureTTL = time.Minute
	// readTimeout bounds one read from one cloud.
	readTimeout = 20 * time.Second
	// fanOut is how many reads run at once.
	fanOut = 8
)

// failedReason is what a person sees for a cloud that didn't answer; the
// cloud's own error goes to the log.
const failedReason = "The cloud didn't answer. Loomplane asks again in a minute."

// classify turns a read's error into a source's status and reason.
func classify(err error) (status, reason string) {
	if err == nil {
		return Reporting, ""
	}
	var u *unsupportedError
	if errors.As(err, &u) {
		return Unsupported, u.reason
	}
	if r, ok := metrics.IsUnavailable(err); ok {
		return Unavailable, r
	}
	return Failed, failedReason
}

// credential reveals a connection's credential, saying why it can't when
// the connection isn't ready.
func (s *Service) credential(ctx context.Context, c *connections.Connection) (*connections.Connection, []byte, error) {
	live, cred, err := s.Connections.Credential(ctx, c.OrgID, c.ID)
	if errors.Is(err, connections.ErrNotReady) {
		return nil, nil, metrics.Unavailablef("Recheck this connection: it's %s, and Loomplane only reads with a ready one", c.Status)
	}
	return live, cred, err
}

// outcome is a read's outcome for telemetry.
func outcome(err error) string {
	status, _ := classify(err)
	return status
}
