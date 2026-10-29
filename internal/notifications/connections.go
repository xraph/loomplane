package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/herald"
	"github.com/xraph/herald/inbox"
	heraldstore "github.com/xraph/herald/store"
	"github.com/xraph/relay/acceptance"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

// kindConnection marks an outbox row about a cloud connection, not a
// deployment.
const kindConnection = "connection"

// connectionSignInNeeded is the webhook and inbox type of a lapsed sign-in.
const connectionSignInNeeded = "connection.sign_in_needed"

// connectionKeylessRefused is the webhook and inbox type of a keyless trust
// the cloud refused.
const connectionKeylessRefused = "connection.keyless_refused"

// Statuses a connection notice carries.
const (
	noticeSignInNeeded = "sign_in_needed"
	noticeFailed       = "failed"
)

// lapseTimeout bounds the database work of ConnectionLapsed. The caller holds
// a renewal slot, so it must not wait on a slow database.
const lapseTimeout = 10 * time.Second

// ConnectionPayload is what a connection notice carries. It names the
// connection and nothing secret.
type ConnectionPayload struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Cloud        string `json:"cloud"`
	Status       string `json:"status"`
}

// ConnectionLapsed queues a notice that a connection needs signing in again,
// for the person who made it and the organization's owners and admins. It only
// writes outbox rows. The drain loop sends everything, so this stays cheap.
// Calling it again with the same at adds nothing.
func (s *Service) ConnectionLapsed(ctx context.Context, orgID, connectionID, name, cloud, createdBy string, at time.Time) error {
	return s.queueConnectionNotice(ctx, orgID, connectionID, name, cloud, createdBy, noticeSignInNeeded, at)
}

// ConnectionKeylessRefused queues a notice that the cloud stopped trusting
// Loomplane for a keyless connection, for the same people as ConnectionLapsed
// and with the same dedupe.
func (s *Service) ConnectionKeylessRefused(ctx context.Context, orgID, connectionID, name, cloud, createdBy string, at time.Time) error {
	return s.queueConnectionNotice(ctx, orgID, connectionID, name, cloud, createdBy, noticeFailed, at)
}

func (s *Service) queueConnectionNotice(ctx context.Context, orgID, connectionID, name, cloud, createdBy, status string, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, lapseTimeout)
	defer cancel()
	rows, err := s.pg.Query(ctx, `SELECT user_id FROM authsome_members WHERE org_id=$1 AND role IN ('owner','admin')`, orgID)
	if err != nil {
		return err
	}
	var recipients []string
	seen := map[string]bool{}
	add := func(user string) {
		if user != "" && !seen[user] {
			seen[user] = true
			recipients = append(recipients, user)
		}
	}
	add(createdBy)
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			_ = rows.Close()
			return err
		}
		add(user)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(ConnectionPayload{ConnectionID: connectionID, Name: name, Cloud: cloud, Status: status})
	if err != nil {
		return err
	}
	prefix := "connection/" + connectionID + "/" + at.UTC().Format(time.RFC3339) + "/"
	keys := make([]string, len(recipients))
	for i, user := range recipients {
		keys[i] = prefix + user
	}
	_, err = s.pg.Exec(ctx, `INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind)
 SELECT k.key,$1,'',k.user_id,k.key,0,$2::jsonb,'connection' FROM unnest($3::text[],$4::text[]) AS k(key,user_id)
 ON CONFLICT(deployment_id,attempt) DO NOTHING`, orgID, string(payload), keys, recipients)
	return err
}

// processConnection delivers one connection notice: the webhook, the inbox
// entry, then the email. Each step is checkpointed like a deployment's.
func (s *Service) processConnection(ctx context.Context, item *outboxItem) error {
	var payload ConnectionPayload
	if len(item.wire) == 0 {
		if err := json.Unmarshal(item.payload, &payload); err != nil {
			return err
		}
		if _, err := s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET wire_payload=payload WHERE id=$1 AND lease_owner=$2 AND wire_payload IS NULL`, item.id, item.owner); err != nil {
			return err
		}
		item.wire = item.payload
	} else if err := json.Unmarshal(item.wire, &payload); err != nil {
		return err
	}
	url := strings.TrimRight(s.cfg.PublicURL, "/") + "/clouds/accounts/" + payload.ConnectionID
	kind, title, body, subject, mail := connectionCopy(payload, url)
	if !item.webhookDone {
		canonical, err := acceptance.CanonicalJSON(item.wire)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canonical)
		_, err = s.relay.SendReliable(ctx, acceptance.Request{Producer: "loomplane", InstallationID: s.cfg.Identity.AppID, SourceKey: lapseSourceKey(item), SourceFingerprint: hex.EncodeToString(sum[:]), AppID: s.cfg.Identity.AppID, OrgID: item.org, TenantID: item.org, Type: kind, Data: canonical})
		if err != nil {
			return err
		}
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET webhook_done=TRUE WHERE id=$1 AND lease_owner=$2`, item.id, item.owner); err != nil {
			return err
		}
		item.webhookDone = true
	}
	p := security.Principal{OrgID: item.org, UserID: item.user}
	email, member, err := s.memberAddress(ctx, p)
	if err != nil {
		return err
	}
	if !member {
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
			err = s.mailStore.CreateNotification(ctx, &inbox.Notification{ID: inboxID, AppID: s.cfg.Identity.AppID, UserID: item.user, Type: kind, Title: title, Body: body, ActionURL: url, CreatedAt: item.created, Metadata: map[string]string{"org_id": item.org, "kind": kindConnection, "connection_id": payload.ConnectionID, "status": payload.Status, "outbox_id": item.id}})
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
	enabled, err := s.emailPreference(ctx, p, kindConnection)
	if err != nil {
		return err
	}
	if !enabled || !s.MailEnabled() {
		_, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET email_state='skipped' WHERE id=$1 AND lease_owner=$2`, item.id, item.owner)
		return err
	}
	res, err := s.herald.Send(ctx, &herald.SendRequest{AppID: s.cfg.Identity.AppID, Channel: "email", To: []string{email}, Subject: subject, Body: mail})
	if deliveryErr := deliveryError(res, err); deliveryErr != nil {
		return deliveryErr
	}
	_, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET email_state='sent' WHERE id=$1 AND lease_owner=$2`, item.id, item.owner)
	return err
}

// connectionCopy is the event type and the words of a connection notice: a
// lapsed sign-in, or a keyless trust the cloud refused.
func connectionCopy(p ConnectionPayload, url string) (kind, title, body, subject, mail string) {
	if p.Status == noticeFailed {
		cloud := connections.CloudTitle(p.Cloud)
		return connectionKeylessRefused,
			p.Name + " lost its keyless access to " + cloud,
			cloud + " no longer trusts Loomplane for this connection. Check the trust you set up there, then check the connection again.",
			"Loomplane: " + p.Name + " can't reach " + cloud,
			p.Name + " lost its keyless access to " + cloud + ".\n\nCheck the connection: " + url
	}
	return connectionSignInNeeded,
		p.Name + " needs you to sign in again",
		"The sign-in for this cloud account lapsed. Sign in again so deployments to it can continue.",
		"Loomplane: sign in to " + p.Name + " again",
		p.Name + " needs you to sign in again.\n\nSign in: " + url
}

// lapseSourceKey names the lapse, not the recipient. Every recipient's row
// carries the same payload, so Relay treats rows after the first as replays and
// subscribers get one event per lapse.
func lapseSourceKey(item *outboxItem) string {
	return strings.TrimSuffix(item.id, "/"+item.user)
}

// memberAddress returns the address of a user who is still a member of the
// organization. A connection belongs to the organization, so there is no
// project to check.
func (s *Service) memberAddress(ctx context.Context, p security.Principal) (email string, member bool, err error) {
	err = s.pg.QueryRow(ctx, `SELECT u.email FROM authsome_users u JOIN authsome_members m ON m.user_id=u.id WHERE m.org_id=$1 AND u.id=$2 AND u.app_id=$3 AND u.deleted_at IS NULL AND NOT u.banned`, p.OrgID, p.UserID, s.cfg.Identity.AppID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up notification recipient: %w", err)
	}
	return email, true, nil
}
