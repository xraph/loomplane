package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/herald/driver"
	heraldstore "github.com/xraph/herald/store"

	"github.com/xraph/loomplane/internal/security"
)

// Plan notices are queued by a trigger on loomplane_plans, not by Enqueue.
const (
	KindPlanWaiting = "plan.waiting"
	KindPlanDecided = "plan.decided"
)

const (
	planPushTTL = 24 * 3600
	// planStatusReady is planning.StatusReady, which this package doesn't import.
	planStatusReady = "ready"
)

// errWaitingPush holds a decided notice back while the waiting push it
// replaces is still on its way.
var errWaitingPush = errors.New("waiting for an earlier push")

// planPayload is what the trigger stores for both plan kinds. Status and
// DecidedBy are the decision, so only plan.decided carries them.
type planPayload struct {
	PlanID        string `json:"plan_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	Revision      string `json:"revision"`
	Status        string `json:"status"`
	DecidedBy     string `json:"decided_by"`
}

// planDecidedCopy is the body of the push that replaces a waiting one.
func planDecidedCopy(status, by string) string {
	switch status {
	case "approved":
		return "Approved by " + by + "."
	case "rejected":
		return "Rejected by " + by + "."
	case "expired":
		return "It expired before anyone decided."
	case "stale":
		return "It's out of date. Plan again."
	case "superseded":
		return "A newer plan replaced it."
	}
	return "It's no longer waiting for approval."
}

func planPath(planID string) string { return "/deployments/" + planID }

// stale says whether a plan.waiting notice is out of date: its plan is no
// longer waiting for a decision, or is gone.
func (s *Service) stale(ctx context.Context, item *outboxItem) (bool, error) {
	var status string
	err := s.pg.QueryRow(ctx, `SELECT status FROM loomplane_plans WHERE id=$1 AND org_id=$2`, item.subject, item.org).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return status != planStatusReady, err
}

// planNames reads the project's and the environment's names and whether the
// environment is production. A missing one is empty.
func (s *Service) planNames(ctx context.Context, item *outboxItem, p planPayload) (project, env string, production bool, err error) {
	var kind string
	err = s.pg.QueryRow(ctx, `SELECT COALESCE((SELECT name FROM loomplane_projects WHERE id=$1 AND org_id=$3),''),
 COALESCE((SELECT name FROM loomplane_environments WHERE id=$2 AND project_id=$1 AND org_id=$3),''),
 COALESCE((SELECT kind FROM loomplane_environments WHERE id=$2 AND project_id=$1 AND org_id=$3),'')`, p.ProjectID, p.EnvironmentID, item.org).Scan(&project, &env, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if env == "" {
		env = p.Environment
	}
	return project, env, kind == "production", err
}

// decider is who decided a plan, as a person would name them.
func (s *Service) decider(ctx context.Context, by string) (string, error) {
	switch {
	case by == "":
		return "someone", nil
	case strings.HasPrefix(by, "loomplane:"):
		return "Loomplane automation", nil
	}
	var first, last, display, username, email string
	err := s.pg.QueryRow(ctx, `SELECT first_name,last_name,display_username,username,email FROM authsome_users WHERE id=$1 AND app_id=$2 AND deleted_at IS NULL`, by, s.cfg.Identity.AppID).Scan(&first, &last, &display, &username, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "someone", nil
	}
	if err != nil {
		return "", err
	}
	for _, name := range []string{strings.TrimSpace(first + " " + last), strings.TrimSpace(display), strings.TrimSpace(username), strings.TrimSpace(email)} {
		if name != "" {
			return name, nil
		}
	}
	return "someone", nil
}

// planMessage is what a plan notice sends: an inbox item, an email and a
// push for plan.waiting, and the words of the push for plan.decided.
func (s *Service) planMessage(ctx context.Context, item *outboxItem) (*outboxMessage, error) {
	var p planPayload
	if err := json.Unmarshal(item.wire, &p); err != nil {
		return nil, err
	}
	project, env, _, err := s.planNames(ctx, item, p)
	if err != nil {
		return nil, err
	}
	if project == "" {
		project = "A plan"
	}
	path := planPath(p.PlanID)
	url := strings.TrimRight(s.cfg.PublicURL, "/") + path
	meta := map[string]string{"org_id": item.org, "project_id": p.ProjectID, "kind": item.kind, "outbox_id": item.id, "subject_id": p.PlanID, "need": security.DeploymentApprove.Name()}
	d := &outboxMessage{sourceKey: item.kind + ":" + item.subject, event: item.kind, project: p.ProjectID, generic: true, needName: security.DeploymentApprove.Name(),
		inboxType: item.kind, actionURL: url, metadata: meta}
	if item.kind == KindPlanDecided {
		by, byErr := s.decider(ctx, p.DecidedBy)
		if byErr != nil {
			return nil, byErr
		}
		d.title, d.body = project+" to "+env, planDecidedCopy(p.Status, by)
		return d, nil
	}
	d.title, d.body = project+" to "+env+" needs your approval", "Review the plan and approve or reject it."
	d.subject, d.emailBody = "Loomplane: "+d.title, d.body+"\n\nOpen it: "+url
	return d, nil
}

// planPush is what a plan notice pushes.
func (s *Service) planPush(ctx context.Context, item *outboxItem, plan *pushPlan) (*pushPlan, error) {
	var p planPayload
	if err := json.Unmarshal(item.wire, &p); err != nil {
		return nil, err
	}
	_, env, production, err := s.planNames(ctx, item, p)
	if err != nil {
		return nil, err
	}
	plan.Subject, plan.ProjectID, plan.EnvID, plan.Path = p.PlanID, p.ProjectID, p.EnvironmentID, planPath(p.PlanID)
	collapse := "plan:" + p.PlanID
	if item.kind == KindPlanWaiting {
		urgency := driver.UrgencyActive
		if production {
			urgency = driver.UrgencyTimeSensitive
		}
		plan.Push = &Push{Class: ClassNeedsYou, Toggle: "push.approvals", Urgency: urgency, Category: CategoryApproval, Actions: []string{ActionApprove, ActionReview},
			Collapse: collapse, Environment: env, Production: production, TTLSeconds: planPushTTL, Data: map[string]string{"revision": p.Revision}, Coverable: true}
		return plan, nil
	}
	// A decided notice goes only to the phones the waiting push reached.
	var state, inboxID string
	var attempts int
	var raw []byte
	err = s.pg.QueryRow(ctx, `SELECT push_state,attempts,push_devices,inbox_id FROM loomplane_notification_outbox WHERE id=$1`, noticeID(KindPlanWaiting, p.PlanID, item.user)).Scan(&state, &attempts, &raw, &inboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case state == PushPending && attempts == 0:
		// Not tried yet. It will find the plan decided and skip itself.
		return nil, nil
	case state == PushPending:
		return nil, errWaitingPush
	case state != PushSent:
		return nil, nil
	}
	reached := map[string]string{}
	if err := json.Unmarshal(raw, &reached); err != nil {
		return nil, err
	}
	var devices []string
	for id, status := range reached {
		if status == "sent" {
			devices = append(devices, id)
		}
	}
	if len(devices) == 0 {
		return nil, nil
	}
	item.inboxID = inboxID
	plan.Push = &Push{Class: ClassUpdate, Urgency: driver.UrgencyPassive, Category: CategoryApproval, Actions: []string{ActionOpen},
		Collapse: collapse, Environment: env, Production: production, TTLSeconds: planPushTTL, Devices: devices}
	return plan, nil
}

// readWaiting marks the recipient's waiting inbox item read once its plan is
// decided. A waiting notice that never got an inbox item is fine.
func (s *Service) readWaiting(ctx context.Context, item *outboxItem) error {
	id, err := notificationID(noticeID(KindPlanWaiting, item.subject, item.user))
	if err != nil {
		return err
	}
	if err = s.mailStore.MarkRead(ctx, id); err != nil && !errors.Is(err, heraldstore.ErrNotificationNotFound) {
		return err
	}
	return nil
}
