package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/security"
)

// Outbox kinds. A deployment's notice is written by a database trigger. The
// others are queued with Enqueue.
const (
	KindDeployment   = "deployment"
	KindAgentRequest = "agent.request"
	KindAgentReport  = "agent.report"
	KindAgentBudget  = "agent.budget"
)

const (
	maxNoticeTitle = 200
	maxNoticeBody  = 2000
)

// Notice is one thing to tell some people about, whatever its kind: an
// inbox item, a webhook for the organization and an email each.
type Notice struct {
	Kind  string
	OrgID string
	// ProjectID is empty for a workspace-wide notice.
	ProjectID string
	// SubjectID makes the notice once per kind, subject and recipient.
	SubjectID  string
	Recipients []string // user IDs
	// Title is at most 200 runes, and the caller has scanned it for secrets.
	Title string
	// Body is at most 2000 runes.
	Body string
	// Path is the dashboard path the notice opens, and starts with "/".
	Path string
	// Metadata holds names and IDs only.
	Metadata map[string]string
	// Need is the permission, as "resource:action", each recipient must
	// hold when the notice is delivered. Empty for none.
	Need string
}

// Querier runs one query on the database or inside a transaction.
type Querier interface {
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
}

// noticePayload is what a generic outbox row stores.
type noticePayload struct {
	ProjectID string            `json:"project_id"`
	SubjectID string            `json:"subject_id"`
	Title     string            `json:"title"`
	Body      string            `json:"body"`
	Path      string            `json:"path"`
	Metadata  map[string]string `json:"metadata"`
	Need      string            `json:"need"`
}

// noticeWire is the payload plus the link, fixed on first delivery.
type noticeWire struct {
	noticePayload
	URL string `json:"url"`
}

// Enqueue queues the notice once for each recipient. Whether each one may
// see it is checked at delivery. Queueing the same notice again adds
// nothing.
func Enqueue(ctx context.Context, q Execer, n Notice) error {
	switch {
	case n.Kind == "" || n.Kind == KindDeployment:
		return fmt.Errorf("%w: a notice needs a kind of its own", ErrInvalid)
	case n.OrgID == "" || n.SubjectID == "":
		return fmt.Errorf("%w: a notice needs an organization and a subject", ErrInvalid)
	case !strings.HasPrefix(n.Path, "/"):
		return fmt.Errorf("%w: a notice path starts with /", ErrInvalid)
	case utf8.RuneCountInString(n.Title) > maxNoticeTitle:
		return fmt.Errorf("%w: a notice title is at most %d characters", ErrInvalid, maxNoticeTitle)
	case utf8.RuneCountInString(n.Body) > maxNoticeBody:
		return fmt.Errorf("%w: a notice body is at most %d characters", ErrInvalid, maxNoticeBody)
	}
	if _, err := parseNeed(n.Need); err != nil {
		return err
	}
	var users []string
	for _, user := range n.Recipients {
		if user != "" && !slices.Contains(users, user) {
			users = append(users, user)
		}
	}
	if len(users) == 0 {
		return nil
	}
	meta := n.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	payload, err := json.Marshal(noticePayload{ProjectID: n.ProjectID, SubjectID: n.SubjectID, Title: n.Title, Body: n.Body, Path: n.Path, Metadata: meta, Need: n.Need})
	if err != nil {
		return err
	}
	ids := make([]string, len(users))
	for i, user := range users {
		ids[i] = noticeID(n.Kind, n.SubjectID, user)
	}
	_, err = q.Exec(ctx, `INSERT INTO loomplane_notification_outbox(id,org_id,project_id,user_id,deployment_id,attempt,payload,kind,subject_id)
 SELECT k.id,$1,$2,k.user_id,k.id,0,$3::jsonb,$4,$5 FROM unnest($6::text[],$7::text[]) AS k(id,user_id)
 ON CONFLICT(deployment_id,attempt) DO NOTHING`, n.OrgID, n.ProjectID, string(payload), n.Kind, n.SubjectID, ids, users)
	return err
}

func noticeID(kind, subject, user string) string { return kind + ":" + subject + ":" + user }

// Admins lists the organization's owners and admins by role, sorted by
// user ID.
func Admins(ctx context.Context, q Querier, orgID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT user_id FROM authsome_members WHERE org_id=$1 AND role IN ('owner','admin') ORDER BY user_id`, orgID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var users []string
	for rows.Next() {
		var user string
		if scanErr := rows.Scan(&user); scanErr != nil {
			return nil, scanErr
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// parseNeed reads "resource:action". The empty string is no need.
func parseNeed(need string) (*security.Permission, error) {
	if need == "" {
		return nil, nil
	}
	resource, action, ok := strings.Cut(need, ":")
	if !ok || resource == "" || action == "" || strings.Contains(action, ":") {
		return nil, fmt.Errorf("%w: a notice need is resource:action", ErrInvalid)
	}
	return &security.Permission{Resource: resource, Action: action}, nil
}

func (s *Service) noticeWire(item *outboxItem) ([]byte, error) {
	var p noticePayload
	if err := json.Unmarshal(item.payload, &p); err != nil {
		return nil, err
	}
	return json.Marshal(noticeWire{noticePayload: p, URL: strings.TrimRight(s.cfg.PublicURL, "/") + p.Path})
}

// genericMessage is what a notice queued with Enqueue sends. The webhook
// is one per notice, so its rows share a source key.
func genericMessage(item *outboxItem) (*outboxMessage, error) {
	var w noticeWire
	if err := json.Unmarshal(item.wire, &w); err != nil {
		return nil, err
	}
	meta := map[string]string{"org_id": item.org, "project_id": w.ProjectID, "kind": item.kind, "outbox_id": item.id, "subject_id": w.SubjectID, "need": w.Need}
	return &outboxMessage{
		sourceKey: item.kind + ":" + item.subject, event: item.kind + ".created", project: w.ProjectID, generic: true, needName: w.Need,
		inboxType: item.kind, title: w.Title, body: w.Body, actionURL: w.URL, metadata: meta,
		subject: "Loomplane: " + w.Title, emailBody: w.Body + "\n\nOpen it: " + w.URL,
	}, nil
}
