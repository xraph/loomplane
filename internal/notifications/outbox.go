package notifications

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/herald"
	heraldid "github.com/xraph/herald/id"
	"github.com/xraph/herald/inbox"
	heraldstore "github.com/xraph/herald/store"
	"github.com/xraph/relay/acceptance"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

type DeploymentPayload struct {
	DeploymentID  string `json:"deployment_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	PlanID        string `json:"plan_id"`
	Status        string `json:"status"`
	Attempt       int    `json:"attempt"`
	URL           string `json:"url"`
}
type outboxItem struct {
	id, org, user, owner   string
	kind                   string
	payload, wire          []byte
	inboxDone, webhookDone bool
	emailState             string
	attempts               int
	created                time.Time
}

func (s *Service) drain(ctx context.Context) error {
	for range 10 {
		item, err := s.claim(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		itemCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err = s.process(itemCtx, item)
		cancel()
		if err != nil {
			// No provider replies, addresses or payloads are retained in diagnostics.
			diagnostic := "notification delivery failed"
			if errors.Is(err, ErrDeliveryFailed) {
				diagnostic = "email delivery failed"
			}
			if _, saveErr := s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET last_error=$3,lease_until=NULL,lease_owner='',next_attempt_at=clock_timestamp()+($4 * interval '1 second'),email_state=CASE WHEN attempts>=10 AND email_state='pending' THEN 'failed' ELSE email_state END WHERE id=$1 AND lease_owner=$2`, item.id, item.owner, diagnostic, int(retryDelay(item.attempts).Seconds())); saveErr != nil {
				return saveErr
			}
			continue
		}
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET completed_at=clock_timestamp(),lease_until=NULL,lease_owner='',last_error='' WHERE id=$1 AND lease_owner=$2`, item.id, item.owner); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) claim(ctx context.Context) (*outboxItem, error) {
	item := &outboxItem{owner: rand.Text()}
	err := s.pg.QueryRow(ctx, `UPDATE loomplane_notification_outbox SET lease_owner=$1,lease_until=clock_timestamp()+interval '2 minutes',attempts=attempts+1 WHERE id=(SELECT id FROM loomplane_notification_outbox WHERE completed_at IS NULL AND attempts<10 AND next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<clock_timestamp()) ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,org_id,user_id,kind,payload,wire_payload,inbox_done,webhook_done,email_state,attempts,created_at`, item.owner).Scan(&item.id, &item.org, &item.user, &item.kind, &item.payload, &item.wire, &item.inboxDone, &item.webhookDone, &item.emailState, &item.attempts, &item.created)
	return item, err
}
func (s *Service) process(ctx context.Context, item *outboxItem) error {
	if item.kind == kindConnection {
		return s.processConnection(ctx, item)
	}
	var payload DeploymentPayload
	if len(item.wire) == 0 {
		if err := json.Unmarshal(item.payload, &payload); err != nil {
			return err
		}
		payload.URL = strings.TrimRight(s.cfg.PublicURL, "/") + "/deployments/" + payload.PlanID
		wire, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET wire_payload=$3 WHERE id=$1 AND lease_owner=$2 AND wire_payload IS NULL`, item.id, item.owner, wire); err != nil {
			return err
		}
		item.wire = wire
	} else if err := json.Unmarshal(item.wire, &payload); err != nil {
		return err
	}
	if !item.webhookDone {
		// JSONB changes whitespace and key order. Fingerprint the canonical
		// payload so replay after a crash matches the original receipt.
		canonical, err := acceptance.CanonicalJSON(item.wire)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canonical)
		_, err = s.relay.SendReliable(ctx, acceptance.Request{Producer: "loomplane", InstallationID: s.cfg.Identity.AppID, SourceKey: item.id, SourceFingerprint: hex.EncodeToString(sum[:]), AppID: s.cfg.Identity.AppID, OrgID: item.org, TenantID: item.org, Type: "deployment." + payload.Status, Data: canonical})
		if err != nil {
			return err
		}
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET webhook_done=TRUE WHERE id=$1 AND lease_owner=$2`, item.id, item.owner); err != nil {
			return err
		}
		item.webhookDone = true
	}
	p := security.Principal{OrgID: item.org, UserID: item.user}
	email, authorized, err := s.recipient(ctx, p, payload.ProjectID)
	if err != nil {
		return err
	}
	if !authorized {
		_, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET inbox_done=TRUE,email_state='skipped' WHERE id=$1 AND lease_owner=$2`, item.id, item.owner)
		return err
	}
	if !item.inboxDone {
		inboxID, idErr := notificationID(item.id)
		if idErr != nil {
			return idErr
		}
		_, err = s.mailStore.GetNotification(ctx, inboxID)
		if errors.Is(err, heraldstore.ErrNotificationNotFound) {
			err = s.mailStore.CreateNotification(ctx, &inbox.Notification{ID: inboxID, AppID: s.cfg.Identity.AppID, UserID: item.user, Type: "deployment." + payload.Status, Title: "Deployment " + payload.Status, Body: "A deployment attempt finished. Open the deployment to review its result.", ActionURL: payload.URL, CreatedAt: item.created, Metadata: map[string]string{"org_id": item.org, "project_id": payload.ProjectID, "status": payload.Status, "outbox_id": item.id}})
		}
		if err != nil {
			return err
		}
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET inbox_done=TRUE WHERE id=$1 AND lease_owner=$2`, item.id, item.owner); err != nil {
			return err
		}
	}
	if item.emailState != "pending" {
		return nil
	}
	enabled, err := s.emailPreference(ctx, p)
	if err != nil {
		return err
	}
	if !enabled || !s.MailEnabled() {
		_, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET email_state='skipped' WHERE id=$1 AND lease_owner=$2`, item.id, item.owner)
		return err
	}
	res, err := s.herald.Send(ctx, &herald.SendRequest{AppID: s.cfg.Identity.AppID, Channel: "email", To: []string{email}, Subject: "Loomplane deployment " + payload.Status, Body: "Your deployment " + payload.Status + ".\n\nReview the deployment: " + payload.URL})
	if deliveryErr := deliveryError(res, err); deliveryErr != nil {
		return deliveryErr
	}
	_, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET email_state='sent' WHERE id=$1 AND lease_owner=$2`, item.id, item.owner)
	return err
}
func (s *Service) recipient(ctx context.Context, p security.Principal, project string) (address string, authorized bool, err error) {
	var email string
	err = s.pg.QueryRow(ctx, `SELECT u.email FROM authsome_users u JOIN authsome_members m ON m.user_id=u.id JOIN loomplane_projects p ON p.org_id=m.org_id WHERE m.org_id=$1 AND u.id=$2 AND p.id=$3 AND u.app_id=$4 AND u.deleted_at IS NULL AND NOT u.banned`, p.OrgID, p.UserID, project, s.cfg.Identity.AppID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if s.authz == nil {
		return "", false, fmt.Errorf("notifications authorization unavailable")
	}
	err = s.authz.Require(ctx, p, security.ProjectRead, project)
	if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrNotMember) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return email, true, nil
}
func notificationID(source string) (heraldid.InboxID, error) {
	sum := sha256.Sum256([]byte("loomplane/notification/" + source))
	raw := hex.EncodeToString(sum[:16])
	uuid := raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	tid, err := typeid.FromUUID("hinb", uuid)
	if err != nil {
		return heraldid.Nil, err
	}
	return heraldid.ParseInboxID(tid.String())
}
