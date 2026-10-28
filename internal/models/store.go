package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/security"
)

// Errors a Store returns. Test with errors.Is.
var (
	ErrNotFound = errors.New("model deployment not found")
	ErrConflict = errors.New("a model of that name is already deployed in this environment")
	ErrStale    = errors.New("the model deployment changed since it was read")
	ErrInvalid  = errors.New("invalid model deployment")
	ErrInUse    = errors.New("the model is in use")
)

// Phases of a model deployment, before its catalog state takes over.
const (
	// PhaseWaitingForServer: the GPU server it needs is being created.
	PhaseWaitingForServer = "waiting_for_server"
	// PhasePlanned: the manifest has it and a plan was made.
	PhasePlanned = "planned"
	// PhaseActive: a deployment ran it. Its catalog state says how it is.
	PhaseActive = "active"
	// PhaseNeedsAttention: waiting for a server went wrong and someone has
	// to retry or cancel.
	PhaseNeedsAttention = "needs_attention"
)

// DeployInput is what someone asked to deploy, kept on the row while the
// model waits for a server so the deploy can carry on once it's there.
type DeployInput struct {
	EntryID          string       `json:"entry_id"`
	Quantization     string       `json:"quantization,omitempty"` // "" picks the best that fits the target
	Target           TargetChoice `json:"target"`
	Resource         string       `json:"resource,omitempty"`      // "" is DefaultResource(entry)
	MaxModelLen      int          `json:"max_model_len,omitempty"` // 0 is the largest that fits, at most 32768
	MonthlyBudgetUSD string       `json:"monthly_budget_usd,omitempty"`
	// ReviewServer asks Preview to review the GPU server a server size
	// target creates. A review asks the provider for its sizes and records
	// an operation in Infrastructure, so the page sends it only from its
	// explicit "Review server" step, never on every change of the form.
	ReviewServer bool `json:"review_server,omitempty"`
	// Server approves the GPU server a server size target creates: the
	// review a ReviewServer preview returned in DeployPreview.Server.
	// Deploy refuses a server size without it.
	Server *ServerApproval `json:"server,omitempty"`
	// Moving is set only by Move, never from a request: the request moves
	// a deployed model, and this is the deployment it moves from, to put
	// back if the move is cancelled. Deploy and Preview clear it. It's
	// kept in the row's request, so it has a JSON name, but it isn't API
	// input: the Task 25 graft of DeployInput leaves it out.
	Moving *MovingFrom `json:"moving,omitempty"`
}

// MovingFrom is a deployment as it was before a move started: its kind,
// where it runs and on how many GPUs, and the row's phase, server and
// plan, to put back if the move onto a GPU server Loomplane creates first
// is cancelled.
type MovingFrom struct {
	Kind              string `json:"kind"`
	ConnectionID      string `json:"connection_id,omitempty"`
	GPUKind           string `json:"gpu_kind,omitempty"`
	GPUs              int    `json:"gpus,omitempty"`
	Phase             string `json:"phase,omitempty"`
	ServerName        string `json:"server_name,omitempty"`
	ServerCloudID     string `json:"server_cloud_id,omitempty"`
	ServerOperationID string `json:"server_operation_id,omitempty"`
	PlanID            string `json:"plan_id,omitempty"`
}

// ServerApproval names the reviewed GPU server operation a deploy
// approves, with the review token that applies it.
type ServerApproval struct {
	CloudID     string `json:"cloud_id"`
	OperationID string `json:"operation_id"`
	Review      string `json:"review"`
}

// Deployment is a model deployed to one environment, as Loomplane tracks
// it: what was asked for, where it's going, and what it costs this month.
// The manifest and the bindings say what runs; a model added by hand in
// YAML gets a row with defaults the first time it's seen.
//
// A deployment runs only in its environment. Money is decimal text, never
// a float. No key or token is ever stored here: the model's API key lives
// in Vault with the resource's secrets.
type Deployment struct {
	ID            string `json:"id"` // typeid "aimodel"
	OrgID         string `json:"-"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"` // where it runs
	Resource      string `json:"resource"`
	EntryID       string `json:"entry_id"`
	Quantization  string `json:"quantization"`
	Engine        string `json:"engine"`
	// MonthlyBudgetUSD is the alert threshold, two places, "" for none.
	MonthlyBudgetUSD string `json:"monthly_budget_usd,omitempty"`
	// HourlyUSDOverride is what the owner says the GPU costs per hour, up
	// to four places, "" to use the known price.
	HourlyUSDOverride string `json:"hourly_usd_override,omitempty"`
	Phase             string `json:"phase"`
	ServerCloudID     string `json:"server_cloud_id,omitempty"`
	ServerOperationID string `json:"server_operation_id,omitempty"`
	// ServerName is the server it waits for or runs on, for messages.
	ServerName string `json:"server_name,omitempty"`
	PlanID     string `json:"plan_id,omitempty"`
	Message    string `json:"message,omitempty"`
	// Request is kept while waiting for a server.
	Request *DeployInput `json:"-"`
	// RequestedBy is who asked, stored privately so the deploy can carry on
	// as them once the server is up.
	RequestedBy security.Principal `json:"-"`
	// Month is the month MonthCostUSD covers, "2026-11". Only Accrue
	// changes it, MonthCostUSD and AccruedAt.
	Month        string    `json:"month"`
	MonthCostUSD string    `json:"month_cost_usd"` // four places
	AccruedAt    time.Time `json:"accrued_at"`
	// AlertedProjected and AlertedCrossed are the months the last "on track
	// to cross" and "crossed" budget notices went out for.
	AlertedProjected string    `json:"-"`
	AlertedCrossed   string    `json:"-"`
	Revision         int64     `json:"revision"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Store keeps model deployments. Every read and write is scoped to an
// organization: another organization's row reads as ErrNotFound.
type Store interface {
	// Create inserts d with Revision 1, giving it an ID when it has none.
	// ErrConflict when its org, project, environment and resource already
	// have a row. On success d holds the stored row.
	Create(ctx context.Context, d *Deployment) error
	// Get returns one row, or ErrNotFound.
	Get(ctx context.Context, orgID, projectID, environmentID, resource string) (*Deployment, error)
	// List lists the organization's rows by project, environment and
	// resource.
	List(ctx context.Context, orgID string) ([]*Deployment, error)
	// ListProject lists one project's rows by environment and resource.
	ListProject(ctx context.Context, orgID, projectID string) ([]*Deployment, error)
	// Update writes d over the row with d's org and ID when the stored
	// Revision is d.Revision, and bumps it. ErrStale otherwise. It never
	// changes the row's project, environment, resource or month's cost:
	// accrual goes through Accrue. On success d holds the stored row.
	Update(ctx context.Context, d *Deployment) error
	// Delete deletes one row, or returns ErrNotFound.
	Delete(ctx context.Context, orgID, projectID, environmentID, resource string) error
	// Accrue adds addUSD to the row's cost for month under the row's lock,
	// starting from zero when month is newer than the stored one, and sets
	// AccruedAt to at. A month older than the stored one is ErrStale, and so
	// is a d.AccruedAt that isn't the stored one: d must come from the read
	// the interval was worked out from, so two runs over the same interval
	// count it once.
	// Accrual leaves Revision alone, so it never makes an edit stale. On
	// success d holds the new month, cost and AccruedAt.
	Accrue(ctx context.Context, d *Deployment, month, addUSD string, at time.Time) error
}

// WaitingLister is the one read across organizations, for the reconcile
// cron that carries waiting deployments on. It's apart from Store so no
// request path can reach another organization's rows by accident.
type WaitingLister interface {
	// Waiting lists the rows waiting for a server in every organization,
	// oldest change first.
	Waiting(ctx context.Context) ([]*Deployment, error)
	// Stranded lists, in every organization, the planned rows a deploy
	// stopped writing: no plan, no message, someone who asked, and no
	// change since before.
	Stranded(ctx context.Context, before time.Time) ([]*Deployment, error)
}

// Money is decimal text, parsed to whole ten-thousandths of a dollar for
// arithmetic, so no amount passes through a float.
const (
	unitsPerUSD = 10000
	usdPlaces   = 4
)

var (
	decimalPattern = regexp.MustCompile(`^(\d+)(?:\.(\d+))?$`)
	monthPattern   = regexp.MustCompile(`^\d{4}-(0[1-9]|1[012])$`)
)

// usdAmount is the limits of one money column: NUMERIC(digits, places).
type usdAmount struct {
	name           string
	digits, places int
}

var (
	budgetAmount   = usdAmount{"monthly_budget_usd", 12, 2}
	overrideAmount = usdAmount{"hourly_usd_override", 10, 4}
	costAmount     = usdAmount{"month_cost_usd", 12, 4}
)

// parse reads s, a non-negative decimal within the column's limits, as
// ten-thousandths of a dollar.
func (a usdAmount) parse(s string) (int64, error) {
	m := decimalPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%w: %s %q isn't an amount in dollars, like 12.50", ErrInvalid, a.name, s)
	}
	whole := strings.TrimLeft(m[1], "0")
	if len(whole) > a.digits-a.places {
		return 0, fmt.Errorf("%w: %s %s is too large", ErrInvalid, a.name, s)
	}
	if len(m[2]) > a.places {
		return 0, fmt.Errorf("%w: %s %s has more than %d decimal places", ErrInvalid, a.name, s, a.places)
	}
	w, err := strconv.ParseInt("0"+whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s %s: %w", ErrInvalid, a.name, s, err)
	}
	frac := m[2] + strings.Repeat("0", usdPlaces-len(m[2]))
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s %s: %w", ErrInvalid, a.name, s, err)
	}
	return w*unitsPerUSD + f, nil
}

// formatUSD writes ten-thousandths of a dollar as decimal text with four
// places, the way Postgres writes a NUMERIC(12,4).
func formatUSD(units int64) string {
	return fmt.Sprintf("%d.%04d", units/unitsPerUSD, units%unitsPerUSD)
}

// validate checks what the table's constraints would refuse, so a bad row
// is ErrInvalid with a reason instead of a database error.
func (d *Deployment) validate() error {
	for _, f := range []struct{ name, value string }{
		{"org", d.OrgID}, {"project", d.ProjectID}, {"environment", d.EnvironmentID},
		{"resource", d.Resource}, {"entry", d.EntryID}, {"quantization", d.Quantization}, {"engine", d.Engine},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: it has no %s", ErrInvalid, f.name)
		}
	}
	switch d.Phase {
	case PhaseWaitingForServer, PhasePlanned, PhaseActive, PhaseNeedsAttention:
	default:
		return fmt.Errorf("%w: phase %q", ErrInvalid, d.Phase)
	}
	if d.MonthlyBudgetUSD != "" {
		units, err := budgetAmount.parse(d.MonthlyBudgetUSD)
		if err != nil {
			return err
		}
		if units == 0 {
			return fmt.Errorf("%w: a monthly budget must be more than 0; leave it empty for none", ErrInvalid)
		}
	}
	if d.HourlyUSDOverride != "" {
		if _, err := overrideAmount.parse(d.HourlyUSDOverride); err != nil {
			return err
		}
	}
	return nil
}
