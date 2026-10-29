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
	"github.com/xraph/herald/driver/push"
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
	// The caps below keep the sealed envelope under push.MaxEnvelopeBytes
	// even when every character is one JSON escapes to six bytes.
	maxDataValue  = 32  // runes, for any data value Loomplane doesn't bound itself
	maxDataServer = 160 // bytes
	maxDataPath   = 120 // bytes
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
	s, n = strings.ToValidUTF8(s, ""), max(n, 0)
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

// cutBytes keeps at most n bytes of s and always returns valid UTF-8.
func cutBytes(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// envelopeData is the data a phone reads from a push, Contract E. Values
// are cut so the envelope can always be sealed: org_name and the extra
// Push.Data values to 32 runes, path to 120 bytes and server to 160.
func envelopeData(e envelopeInput) map[string]string {
	out := map[string]string{}
	if e.Push != nil {
		for k, v := range e.Push.Data {
			out[k] = cut(v, maxDataValue)
		}
	}
	e.OrgName = cut(e.OrgName, maxDataValue)
	e.Path = cutBytes(e.Path, maxDataPath)
	e.Server = cutBytes(e.Server, maxDataServer)
	out["server"], out["org"], out["org_name"], out["kind"] = e.Server, e.OrgID, e.OrgName, e.Kind
	out["subject"], out["inbox_id"], out["path"], out["project"] = e.Subject, e.InboxID, e.Path, e.ProjectID
	out["sent_at"] = e.SentAt.UTC().Format(time.RFC3339)
	out["urgency"], out["class"], out["actions"], out["environment"] = "", "", "", ""
	if e.Push != nil {
		out["urgency"], out["class"], out["actions"], out["environment"] = string(e.Push.Urgency), e.Push.Class, strings.Join(e.Push.Actions, ","), cut(e.Push.Environment, maxDataValue)
	}
	return out
}

// fitEnvelope cuts the title and body to their limits, then trims the body
// and the title further until Herald can seal the envelope. Characters JSON
// escapes take up to six bytes each, so the limits alone don't promise it.
func fitEnvelope(title, body, category, thread string, data map[string]string) (fittedTitle, fittedBody string, err error) {
	title, body = cut(title, maxPushTitle), cut(body, maxPushBody)
	for {
		_, err = push.MarshalEnvelope(push.Envelope{Title: title, Body: body, Category: category, Thread: thread, Data: data})
		if !errors.Is(err, push.ErrPayloadTooLarge) {
			return title, body, err
		}
		switch {
		case body != "":
			body = cut(body, utf8.RuneCountInString(body)-10)
		case title != "":
			title = cut(title, utf8.RuneCountInString(title)-10)
		default:
			return "", "", err
		}
	}
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
	if err := s.pushUpdate(ctx, item, `UPDATE loomplane_notification_outbox SET push_state=$3 WHERE id=$1 AND lease_owner=$2`, state); err != nil {
		return err
	}
	item.pushState = state
	return nil
}

// pushUpdate runs one of the push step's updates on the row this worker
// holds. Another worker holding it means the lease ran out mid-step: that is
// logged by ID and fails the step, and the new holder starts from what the
// row records.
func (s *Service) pushUpdate(ctx context.Context, item *outboxItem, query string, args ...any) error {
	res, err := s.pg.Exec(ctx, query, append([]any{item.id, item.owner}, args...)...)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n == 0 {
		slog.WarnContext(ctx, "push step lost its lease on an outbox row", "outbox", item.id)
		return errLeaseLost
	}
	return nil
}

var errLeaseLost = errors.New("outbox lease lost")

// pushStep sends one row's push to the person's phones, once each. Whatever
// goes wrong comes back wrapping ErrPushFailed, after recording the phones
// that did get it, so the caller can run the email step and the retry
// reaches only the rest.
//
// A crash between Herald's send and recording push_devices makes the retry
// send again. Every send of a row carries the same collapse ID, so the
// phone replaces the first notification and doesn't show two.
func (s *Service) pushStep(ctx context.Context, item *outboxItem, plan *pushPlan) error {
	err := s.deliverPush(ctx, item, plan)
	if err != nil && !errors.Is(err, ErrPushFailed) {
		slog.WarnContext(ctx, "push step failed", "outbox", item.id, "error", err)
		return fmt.Errorf("%w: %w", ErrPushFailed, err)
	}
	return err
}

func (s *Service) deliverPush(ctx context.Context, item *outboxItem, plan *pushPlan) error {
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
	data := envelopeData(envelopeInput{Server: strings.TrimRight(s.cfg.PublicURL, "/"), OrgID: item.org, OrgName: orgName, Kind: plan.Kind, Subject: plan.Subject,
		InboxID: item.inboxID, Path: plan.Path, ProjectID: plan.ProjectID, Push: p, SentAt: time.Now()})
	title, body, err := fitEnvelope(plan.Title, plan.Body, p.Category, thread, data)
	if err != nil {
		return err
	}
	res, err := s.herald.Send(ctx, &herald.SendRequest{
		AppID: appID, OrgID: item.org, UserID: item.user, Channel: "push", Subject: title, Body: body,
		Push: &driver.PushOptions{
			Urgency: p.Urgency, Kind: driver.PushKindShow, CollapseID: collapse, TTL: time.Duration(p.TTLSeconds) * time.Second,
			ThreadID: thread, Category: p.Category, Data: data,
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
			// DeviceResult carries only the error text, so a dead address
			// is recognised by Herald's own sentinel message.
			if strings.Contains(r.Error, driver.ErrAddressGone.Error()) {
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
		if err := s.pushUpdate(ctx, item, `UPDATE loomplane_notification_outbox SET push_devices=push_devices||$3::jsonb,push_collapse=$4 WHERE id=$1 AND lease_owner=$2`, string(raw), collapse); err != nil {
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
