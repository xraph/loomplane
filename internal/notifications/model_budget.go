package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
)

// kindModelBudget is the outbox kind of a model's budget notice. Only
// admins, who hold ai:manage, get one.
const kindModelBudget = "model.budget"

// Kinds of a model budget notice.
const (
	// ModelBudgetProjected: the model is on track to pass its budget this
	// month.
	ModelBudgetProjected = "projected"
	// ModelBudgetCrossed: what it cost this month passed its budget.
	ModelBudgetCrossed = "crossed"
)

// ModelBudgetNotice says a GPU model is on track to pass, or passed, its
// monthly budget. Money is decimal text to the cent. ProjectedUSD is for
// a "projected" notice, MonthToDateUSD for a "crossed" one.
type ModelBudgetNotice struct {
	OrgID, ProjectID, EnvironmentID, DeploymentID, Resource, Month, Kind string
	ProjectedUSD, BudgetUSD, MonthToDateUSD                              string
}

// Execer runs one statement on the database or inside a transaction. A
// grove driver.Tx is one, and so is the service's own pool.
type Execer interface {
	Exec(ctx context.Context, query string, args ...any) (driver.Result, error)
}

type modelBudgetPayload struct {
	DeploymentID   string `json:"deployment_id"`
	ProjectID      string `json:"project_id"`
	EnvironmentID  string `json:"environment_id"`
	Resource       string `json:"resource"`
	Month          string `json:"month"`
	Kind           string `json:"kind"`
	ProjectedUSD   string `json:"projected_usd,omitempty"`
	BudgetUSD      string `json:"budget_usd"`
	MonthToDateUSD string `json:"month_to_date_usd,omitempty"`
	URL            string `json:"url,omitempty"`
}

// modelBudgetKey names a notice by its model, month, kind and budget. The
// budget is part of it because outbox rows are never deleted and a key
// already there queues nothing: a model whose budget changed gets the new
// budget's notices.
func modelBudgetKey(deploymentID, month, kind, budget string) string {
	return "model-budget:" + deploymentID + ":" + month + ":" + kind + ":" + budget
}

// EnqueueModelBudgetNotice queues the notice for every member of the
// organization. Whether each one may see it, ai:manage, is checked at
// delivery. Sending the same notice again adds nothing.
func (s *Service) EnqueueModelBudgetNotice(ctx context.Context, n ModelBudgetNotice) error {
	return s.EnqueueModelBudgetNoticeIn(ctx, s.pg, n)
}

// EnqueueModelBudgetNoticeIn is EnqueueModelBudgetNotice inside tx, so the
// caller can record that the notice went out in the same transaction.
func (s *Service) EnqueueModelBudgetNoticeIn(ctx context.Context, tx Execer, n ModelBudgetNotice) error {
	if n.Kind != ModelBudgetProjected && n.Kind != ModelBudgetCrossed {
		return fmt.Errorf("%w: model budget notice kind %q", ErrInvalid, n.Kind)
	}
	if n.OrgID == "" || n.ProjectID == "" || n.EnvironmentID == "" || n.DeploymentID == "" || n.Resource == "" || n.Month == "" || n.BudgetUSD == "" {
		return fmt.Errorf("%w: a model budget notice needs its model, month and budget", ErrInvalid)
	}
	payload, err := json.Marshal(modelBudgetPayload{
		DeploymentID: n.DeploymentID, ProjectID: n.ProjectID, EnvironmentID: n.EnvironmentID, Resource: n.Resource,
		Month: n.Month, Kind: n.Kind, ProjectedUSD: n.ProjectedUSD, BudgetUSD: n.BudgetUSD, MonthToDateUSD: n.MonthToDateUSD,
	})
	if err != nil {
		return err
	}
	return s.enqueueMembersIn(ctx, tx, n.OrgID, n.ProjectID, modelBudgetKey(n.DeploymentID, n.Month, n.Kind, n.BudgetUSD), kindModelBudget, payload)
}

// modelURL is the model's page.
func (s *Service) modelURL(projectID, environmentID, resource string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/projects/" + url.PathEscape(projectID) + "/ai/models/" + url.PathEscape(resource) +
		"?environment=" + url.QueryEscape(environmentID)
}

// modelBudgetWire is what a budget notice's webhook carries, fixed on
// first delivery like a deployment's.
func (s *Service) modelBudgetWire(item *outboxItem) ([]byte, error) {
	var p modelBudgetPayload
	if err := json.Unmarshal(item.payload, &p); err != nil {
		return nil, err
	}
	p.URL = s.modelURL(p.ProjectID, p.EnvironmentID, p.Resource)
	return json.Marshal(p)
}

// modelBudgetCopy is a budget notice's title and body. Loomplane never
// stops a model over its budget, and the body says so.
func modelBudgetCopy(p modelBudgetPayload) (title, body string) {
	const after = " Loomplane won't stop it. Open the model to change where it runs or remove it."
	if p.Kind == ModelBudgetCrossed {
		title = fmt.Sprintf("%s passed its $%s budget", p.Resource, p.BudgetUSD)
		if p.MonthToDateUSD == "" {
			return title, strings.TrimSpace(after)
		}
		return title, fmt.Sprintf("It has cost $%s so far this month.", p.MonthToDateUSD) + after
	}
	return fmt.Sprintf("%s is on track to cost $%s this month", p.Resource, p.ProjectedUSD), fmt.Sprintf("Its budget is $%s.", p.BudgetUSD) + after
}

func modelBudgetMessage(item *outboxItem) (*outboxMessage, error) {
	var p modelBudgetPayload
	if err := json.Unmarshal(item.wire, &p); err != nil {
		return nil, err
	}
	if p.Kind != ModelBudgetProjected && p.Kind != ModelBudgetCrossed {
		return nil, fmt.Errorf("unknown model budget notice kind %q", p.Kind)
	}
	title, body := modelBudgetCopy(p)
	meta := map[string]string{
		"org_id": item.org, "project_id": p.ProjectID, "kind": kindModelBudget, "outbox_id": item.id,
		"status": "budget_" + p.Kind, "deployment_id": p.DeploymentID,
	}
	// The organization gets one webhook however many admins it has, so
	// the rows share the notice's key.
	return &outboxMessage{
		sourceKey: modelBudgetKey(p.DeploymentID, p.Month, p.Kind, p.BudgetUSD), event: "model.budget_" + p.Kind, project: p.ProjectID,
		need: &security.AIManage, inboxType: kindModelBudget, title: title, body: body, actionURL: p.URL, metadata: meta,
		subject: "Loomplane: " + title, emailBody: body + "\n\nOpen the model: " + p.URL, alwaysEmail: true,
	}, nil
}
