package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/herald/driver"
)

const (
	failedDeploymentTTL = 12 * 3600
	liveDeploymentTTL   = 3600
	workspaceTTL        = 72 * 3600
)

// pushPlanFor says what a row pushes. Nil means nothing: the kind has no
// push, or its notice asked for none.
func (s *Service) pushPlanFor(ctx context.Context, item *outboxItem, msg *outboxMessage) (*pushPlan, error) {
	plan := &pushPlan{Title: msg.title, Body: msg.body, ProjectID: msg.project, Kind: msg.inboxType,
		Path: strings.TrimPrefix(msg.actionURL, strings.TrimRight(s.cfg.PublicURL, "/"))}
	switch item.kind {
	case KindDeployment:
		return s.deploymentPush(ctx, item, plan)
	case kindModelBudget, kindRenewal, kindRegistration, kindConnection:
		plan.Push = &Push{Class: ClassWorkspace, Toggle: "push.workspace", Urgency: driver.UrgencyActive, Category: CategoryUpdate, Actions: []string{ActionOpen}, TTLSeconds: workspaceTTL}
		return plan, nil
	case KindPlanWaiting, KindPlanDecided:
		return s.planPush(ctx, item, plan)
	case KindReleaseRolledBack, KindReleaseRevertFailed, KindReleaseProgress:
		return s.releasePush(ctx, item, plan)
	}
	var n noticePayload
	if err := json.Unmarshal(item.payload, &n); err != nil {
		return nil, err
	}
	if n.Push == nil {
		return nil, nil
	}
	plan.Push, plan.Subject = n.Push, n.SubjectID
	return plan, nil
}

// deploymentPush names the project and environment, which the row only
// holds by ID, and picks the push for how the deployment ended.
func (s *Service) deploymentPush(ctx context.Context, item *outboxItem, plan *pushPlan) (*pushPlan, error) {
	var payload DeploymentPayload
	if err := json.Unmarshal(item.wire, &payload); err != nil {
		return nil, err
	}
	var project, env, kind string
	err := s.pg.QueryRow(ctx, `SELECT COALESCE((SELECT name FROM loomplane_projects WHERE id=$1 AND org_id=$3),''),
 COALESCE((SELECT name FROM loomplane_environments WHERE id=$2 AND project_id=$1 AND org_id=$3),''),
 COALESCE((SELECT kind FROM loomplane_environments WHERE id=$2 AND project_id=$1 AND org_id=$3),'')`, payload.ProjectID, payload.EnvironmentID, item.org).Scan(&project, &env, &kind)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	production := kind == "production"
	plan.Subject, plan.ProjectID, plan.EnvID = payload.DeploymentID, payload.ProjectID, payload.EnvironmentID
	collapse := "deployment:" + payload.DeploymentID
	switch payload.Status {
	case "failed":
		toggle, urgency := "push.failed", driver.UrgencyActive
		if production {
			toggle, urgency = "push.alerts", driver.UrgencyTimeSensitive
		}
		plan.Title = project + ": deployment to " + env + " failed"
		plan.Push = &Push{Class: ClassAlert, Toggle: toggle, Urgency: urgency, Category: CategoryAlert, Actions: []string{ActionOpen, ActionRetry},
			Collapse: collapse, Environment: env, Production: production, TTLSeconds: failedDeploymentTTL, Coverable: production}
	case "succeeded":
		plan.Title = project + " is live in " + env
		plan.Push = &Push{Class: ClassUpdate, Toggle: "push.live", Urgency: driver.UrgencyPassive, Category: CategoryUpdate, Actions: []string{ActionOpen},
			Collapse: collapse, Environment: env, Production: production, TTLSeconds: liveDeploymentTTL}
	default:
		return nil, nil
	}
	if project == "" || env == "" {
		plan.Title = fallbackTitle(plan)
	}
	return plan, nil
}

// fallbackTitle is the title to use when a project or environment is gone.
func fallbackTitle(plan *pushPlan) string {
	return "Deployment " + strings.TrimPrefix(plan.Kind, "deployment.")
}
