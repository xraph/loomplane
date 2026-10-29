package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/herald"
	"github.com/xraph/herald/device"
	"github.com/xraph/herald/driver"
	"github.com/xraph/herald/message"
)

// Classes say why a notice reaches a phone, and with it how loud a person's
// level setting lets it be.
const (
	ClassNeedsYou  = "needs_you"
	ClassAlert     = "alert"
	ClassUpdate    = "update"
	ClassWorkspace = "workspace"
)

// Categories are the action sets a phone shows with a push.
const (
	CategoryAlert    = "alert"
	CategoryApproval = "approval"
	CategoryAgent    = "agent"
	CategoryUpdate   = "update"
)

// Actions a push can offer.
const (
	ActionApprove  = "approve"
	ActionReview   = "review"
	ActionOpen     = "open"
	ActionRetry    = "retry"
	ActionRollback = "rollback"
	ActionAsk      = "ask"
	ActionTake     = "take"
)

// Push states of an outbox row.
const (
	PushPending = "pending"
	PushSent    = "sent"
	PushSkipped = "skipped"
	PushFailed  = "failed"
)

const (
	maxPushTitle = 80
	maxPushBody  = 240
	// deviceGone is what a row records for a phone Herald dropped because
	// its address is dead. Asking it again would change nothing.
	deviceGone = "gone"
)

// Push says how a notice reaches phones. On a Notice, nil sends no push.
type Push struct {
	Class       string            `json:"class"`
	Toggle      string            `json:"toggle,omitempty"` // a Contract D settings key, "" for none
	Urgency     driver.Urgency    `json:"urgency"`
	Category    string            `json:"category"`
	Actions     []string          `json:"actions,omitempty"`
	Collapse    string            `json:"collapse,omitempty"`    // subject key such as "report:<id>"; "" means "notice:<outbox id>"
	Environment string            `json:"environment,omitempty"` // environment name, "" for workspace notices
	Production  bool              `json:"production,omitempty"`
	TTLSeconds  int               `json:"ttl_seconds,omitempty"`
	Data        map[string]string `json:"data,omitempty"`      // extra Contract E keys: revision, taken_by
	Coverable   bool              `json:"coverable,omitempty"` // ask PushCover before pushing
	Devices     []string          `json:"devices,omitempty"`   // only these device IDs, ignoring levels and toggles
}

// PushCover lets a later feature speak for a notice's push. Nil, the
// default and what a server with AI off has, covers nothing.
type PushCover interface {
	Covers(ctx context.Context, q CoverQuery) (bool, error)
}

// CoverQuery describes a coverable push.
type CoverQuery struct {
	OrgID, Kind, SubjectID, ProjectID, EnvironmentID string
	Production                                       bool
}

// SetPushCover sets what may cover coverable pushes. Call it before Run.
func (s *Service) SetPushCover(c PushCover) { s.pushCover = c }

// pushPlan is what one outbox row pushes.
type pushPlan struct {
	Push                 *Push
	Title, Body, Subject string // Subject is the envelope "subject" (the plan, deployment or report ID)
	ProjectID, EnvID     string
	Path                 string
	Kind                 string // the row's inbox type, which is the envelope "kind"
}

type envelopeInput struct {
	Server, OrgID, OrgName, Kind, Subject, InboxID, Path, ProjectID string
	Push                                                            *Push
	SentAt                                                          time.Time
}

// collapseID is the 16 hex characters a phone replaces a notification by.
func collapseID(subjectKey string) string {
	sum := sha256.Sum256([]byte(subjectKey))
	return hex.EncodeToString(sum[:])[:16]
}

// cut keeps at most n runes of s and always returns valid UTF-8.
func cut(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// allowDevice applies a phone's own settings, with DefaultSettings filling
// what it never set.
func allowDevice(d *device.Device, p *Push) bool {
	if len(p.Devices) > 0 {
		return slices.Contains(p.Devices, d.ID)
	}
	setting := func(key string) string {
		if v, ok := d.Settings[key]; ok && v != "" {
			return v
		}
		return DefaultSettings[key]
	}
	if p.Toggle != "" && setting(p.Toggle) != "true" {
		return false
	}
	if p.Class == ClassWorkspace {
		return true
	}
	level := setting("level.staging")
	if p.Production {
		level = setting("level.production")
	}
	switch level {
	case "everything":
		return true
	case "critical":
		return p.Class == ClassNeedsYou || p.Class == ClassAlert
	}
	return false
}

// envelopeData is the data a phone reads from a push, Contract E.
func envelopeData(e envelopeInput) map[string]string {
	out := map[string]string{}
	if e.Push != nil {
		maps.Copy(out, e.Push.Data)
	}
	out["server"], out["org"], out["org_name"], out["kind"] = e.Server, e.OrgID, e.OrgName, e.Kind
	out["subject"], out["inbox_id"], out["path"], out["project"] = e.Subject, e.InboxID, e.Path, e.ProjectID
	out["sent_at"] = e.SentAt.UTC().Format(time.RFC3339)
	out["urgency"], out["class"], out["actions"], out["environment"] = "", "", "", ""
	if e.Push != nil {
		out["urgency"], out["class"], out["actions"], out["environment"] = string(e.Push.Urgency), e.Push.Class, strings.Join(e.Push.Actions, ","), e.Push.Environment
	}
	return out
}

func pushDone(status string) bool {
	return status == string(message.StatusSent) || status == string(message.StatusSuppressed) || status == string(message.StatusFiltered)
}

func (s *Service) orgName(ctx context.Context, org string) (string, error) {
	var name string
	err := s.pg.QueryRow(ctx, `SELECT name FROM authsome_organizations WHERE id=$1`, org).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// finishPush settles the row's push when nothing is left to send. A row
// that already got a phone counts as sent, whatever happened to the rest,
// and any other row is skipped.
func (s *Service) finishPush(ctx context.Context, item *outboxItem) error {
	state := PushSkipped
	if slices.Contains(slices.Collect(maps.Values(item.pushDevices)), string(message.StatusSent)) {
		state = PushSent
	}
	if _, err := s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET push_state=$3 WHERE id=$1 AND lease_owner=$2`, item.id, item.owner, state); err != nil {
		return err
	}
	item.pushState = state
	return nil
}

// pushStep sends one row's push to the person's phones, once each. It
// returns ErrPushFailed when a phone didn't take it, after recording the
// phones that did, so the retry reaches only the rest.
func (s *Service) pushStep(ctx context.Context, item *outboxItem, plan *pushPlan) error {
	if item.pushState != PushPending {
		return nil
	}
	if !s.PushEnabled() || plan == nil || plan.Push == nil {
		return s.finishPush(ctx, item)
	}
	p := plan.Push
	if p.Coverable && s.pushCover != nil {
		covered, err := s.pushCover.Covers(ctx, CoverQuery{OrgID: item.org, Kind: plan.Kind, SubjectID: plan.Subject, ProjectID: plan.ProjectID, EnvironmentID: plan.EnvID, Production: p.Production})
		if err != nil {
			return err
		}
		if covered {
			return s.finishPush(ctx, item)
		}
	}
	appID := s.cfg.Identity.AppID
	devices, err := s.herald.ListDevices(ctx, appID, item.user)
	if err != nil {
		return err
	}
	wanted := func(d *device.Device) bool {
		return d.Channel == "push" && !pushDone(item.pushDevices[d.ID]) && allowDevice(d, p)
	}
	if !slices.ContainsFunc(devices, wanted) {
		return s.finishPush(ctx, item)
	}
	orgName, err := s.orgName(ctx, item.org)
	if err != nil {
		return err
	}
	key := p.Collapse
	if key == "" {
		key = "notice:" + item.id
	}
	collapse := collapseID(key)
	thread := plan.ProjectID
	if thread == "" {
		thread = item.org
	}
	res, err := s.herald.Send(ctx, &herald.SendRequest{
		AppID: appID, OrgID: item.org, UserID: item.user, Channel: "push",
		Subject: cut(plan.Title, maxPushTitle), Body: cut(plan.Body, maxPushBody),
		Push: &driver.PushOptions{
			Urgency: p.Urgency, Kind: driver.PushKindShow, CollapseID: collapse, TTL: time.Duration(p.TTLSeconds) * time.Second,
			ThreadID: thread, Category: p.Category,
			Data: envelopeData(envelopeInput{Server: strings.TrimRight(s.cfg.PublicURL, "/"), OrgID: item.org, OrgName: orgName, Kind: plan.Kind, Subject: plan.Subject,
				InboxID: item.inboxID, Path: plan.Path, ProjectID: plan.ProjectID, Push: p, SentAt: time.Now()}),
		},
		DeviceFilter: wanted,
	})
	if err != nil {
		return err
	}
	results := map[string]string{}
	failed := 0
	for _, r := range res.Devices {
		switch r.Status {
		case message.StatusSent, message.StatusSuppressed:
			results[r.DeviceID] = string(r.Status)
		case message.StatusFailed:
			if strings.Contains(r.Error, "address gone") {
				results[r.DeviceID] = deviceGone
				continue
			}
			results[r.DeviceID] = string(r.Status)
			failed++
		}
	}
	if len(results) > 0 {
		raw, marshalErr := json.Marshal(results)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = s.pg.Exec(ctx, `UPDATE loomplane_notification_outbox SET push_devices=push_devices||$3::jsonb,push_collapse=$4 WHERE id=$1 AND lease_owner=$2`, item.id, item.owner, string(raw), collapse); err != nil {
			return err
		}
		maps.Copy(item.pushDevices, results)
	}
	if failed > 0 {
		slog.WarnContext(ctx, "push delivery failed for some phones", "outbox", item.id, "devices", failed)
		return fmt.Errorf("%w", ErrPushFailed)
	}
	return s.finishPush(ctx, item)
}
