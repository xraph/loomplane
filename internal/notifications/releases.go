package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xraph/herald/driver"
)

// Release notices are queued by a trigger on loomplane_deployments, not by
// Enqueue. The kinds share the release. prefix, so revert_failed is
// release.revert_failed here though the canary status it comes from is
// still revert_failed.
const (
	KindReleaseRolledBack   = "release.rolled_back"
	KindReleaseRevertFailed = "release.revert_failed"
	KindReleaseProgress     = "release.progress"
)

// canaryPromoted is deployments.CanaryPromoted, which this package doesn't import.
const canaryPromoted = "promoted"

// releasePayload is what the trigger stores for every release kind. Step
// is the canary step the notice is about, or the number of steps once the
// release is promoted. Weight is the traffic share the new release has or
// had when it failed.
type releasePayload struct {
	DeploymentID   string `json:"deployment_id"`
	ProjectID      string `json:"project_id"`
	EnvironmentID  string `json:"environment_id"`
	PlanID         string `json:"plan_id"`
	Status         string `json:"status"` // canary status
	Step           int    `json:"step"`
	Weight         int    `json:"weight"`
	PreviousWeight int    `json:"previous_weight"`
	Reason         string `json:"reason"`
}

func releasePath(planID string) string { return "/deployments/" + planID }

// releaseCopy is the words of a release notice.
func releaseCopy(kind string, p releasePayload, project, environment string) (title, body string) {
	switch kind {
	case KindReleaseRolledBack:
		body = "The new release failed its health check at " + fmt.Sprint(p.Weight) + "%, so Loomplane moved all traffic back to the previous one."
		if p.Weight <= 0 {
			body = "The new release failed its health check, so Loomplane moved all traffic back to the previous one."
		}
		return project + ": canary rolled back in " + environment, body
	case KindReleaseRevertFailed:
		return project + ": rollback failed in " + environment, "The canary failed its health check and Loomplane couldn't move traffic back. Some requests may still reach the new release."
	}
	if p.Status == canaryPromoted {
		return project + ": release promoted in " + environment, "Every canary step passed and the new release takes all traffic."
	}
	title = fmt.Sprintf("%s: canary at %d%% in %s", project, p.Weight, environment)
	if p.Step == 0 {
		return title, fmt.Sprintf("Loomplane sent %d%% of traffic to the new release and is watching its health.", p.Weight)
	}
	return title, fmt.Sprintf("It held up at %d%%, so Loomplane moved it to %d%%.", p.PreviousWeight, p.Weight)
}

// releaseMessage is what a release notice sends: an inbox item and an email
// for the kinds that mail, and the words of the push.
func (s *Service) releaseMessage(ctx context.Context, item *outboxItem) (*outboxMessage, error) {
	var p releasePayload
	if err := json.Unmarshal(item.wire, &p); err != nil {
		return nil, err
	}
	project, env, _, err := s.planNames(ctx, item, planPayload{ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID})
	if err != nil {
		return nil, err
	}
	if project == "" {
		project = "A release"
	}
	if env == "" {
		env = "its environment"
	}
	title, body := releaseCopy(item.kind, p, project, env)
	url := strings.TrimRight(s.cfg.PublicURL, "/") + releasePath(p.PlanID)
	meta := map[string]string{"org_id": item.org, "project_id": p.ProjectID, "kind": item.kind, "outbox_id": item.id, "subject_id": p.DeploymentID}
	return &outboxMessage{sourceKey: item.kind + ":" + item.subject, event: item.kind, project: p.ProjectID, generic: true,
		inboxType: item.kind, title: title, body: body, actionURL: url, metadata: meta,
		subject: "Loomplane: " + title, emailBody: body + "\n\nOpen it: " + url}, nil
}

// releasePush is what a release notice pushes. Every kind of one
// deployment shares a collapse ID, so a phone shows the latest.
func (s *Service) releasePush(ctx context.Context, item *outboxItem, plan *pushPlan) (*pushPlan, error) {
	var p releasePayload
	if err := json.Unmarshal(item.wire, &p); err != nil {
		return nil, err
	}
	_, env, production, err := s.planNames(ctx, item, planPayload{ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID})
	if err != nil {
		return nil, err
	}
	plan.Subject, plan.ProjectID, plan.EnvID, plan.Path = p.DeploymentID, p.ProjectID, p.EnvironmentID, releasePath(p.PlanID)
	push := &Push{Collapse: "release:" + p.DeploymentID, Environment: env, Production: production, Actions: []string{ActionOpen}}
	switch item.kind {
	case KindReleaseRolledBack:
		push.Urgency = driver.UrgencyActive
		if production {
			push.Urgency = driver.UrgencyTimeSensitive
		}
		push.Class, push.Toggle, push.Category, push.TTLSeconds, push.Coverable = ClassAlert, "push.rollbacks", CategoryAlert, failedDeploymentTTL, true
	case KindReleaseRevertFailed:
		push.Class, push.Toggle, push.Urgency, push.Category, push.TTLSeconds = ClassAlert, "push.alerts", driver.UrgencyCritical, CategoryAlert, failedDeploymentTTL
	default:
		push.Class, push.Toggle, push.Urgency, push.Category, push.TTLSeconds = ClassUpdate, "push.progress", driver.UrgencyPassive, CategoryUpdate, liveDeploymentTTL
	}
	plan.Push = push
	return plan, nil
}
