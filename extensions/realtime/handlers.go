package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xraph/forge"

	store "github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"
)

type Envelope struct {
	Event   string `json:"event"`
	Data    any    `json:"data"`
	Cursor  string `json:"cursor"`
	Channel string `json:"channel"`
}

type Status struct {
	Cursor string    `json:"cursor"`
	Time   time.Time `json:"time"`
}

// Snapshot describes the public wire contract. The store retains raw JSON;
// client schemas describe each named payload as JSON rather than base64 text.
type Snapshot struct {
	Cursor  string           `json:"cursor"`
	Records []SnapshotRecord `json:"records"`
	Partial bool             `json:"partial"`
}

type SnapshotRecord struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	ProjectID string    `json:"project_id,omitempty"`
	Event     string    `json:"event"`
	EntityID  string    `json:"entity_id"`
	Data      any       `json:"data"`
	Time      time.Time `json:"time"`
}

func permission(event string) security.Permission {
	switch strings.SplitN(event, ".", 2)[0] {
	case "environment":
		return security.EnvironmentRead
	case "plan", "deployment", "build", "cloud_sync":
		return security.DeploymentRead
	case "binding":
		return security.InfrastructureRead
	case "connection", "cloud", "cloud_target", "environment_cloud":
		return security.CloudAccountRead
	default:
		return security.ProjectRead
	}
}

func (e *Extension) checkScope(ctx context.Context, p security.Principal, sub Subscription) error {
	for _, id := range sub.ProjectIDs {
		project, err := e.store.ProjectFor(ctx, p.OrgID, "project", id)
		if err != nil {
			return err
		}
		if err := e.authz.Require(ctx, p, security.ProjectRead, project); err != nil {
			return err
		}
	}
	for _, id := range sub.DeploymentIDs {
		project, err := e.store.ProjectFor(ctx, p.OrgID, "deployment", id)
		if err != nil {
			return err
		}
		if len(sub.ProjectIDs) != 0 && !slices.Contains(sub.ProjectIDs, project) {
			return store.ErrNotFound
		}
		if err := e.authz.Require(ctx, p, security.DeploymentRead, project); err != nil {
			return err
		}
	}
	return nil
}

// filter checks each distinct grant once per batch, never across batches.
func (e *Extension) filter(ctx context.Context, p security.Principal, sub Subscription, events []store.Event) ([]store.Event, error) {
	out := []store.Event{}
	grants := map[string]bool{}
	for _, event := range events {
		if event.ProjectID != "" && len(sub.ProjectIDs) != 0 && !slices.Contains(sub.ProjectIDs, event.ProjectID) {
			continue
		}
		if strings.HasPrefix(event.Type, "build.") {
			var ref struct {
				DeploymentID string `json:"deployment_id"`
			}
			if err := json.Unmarshal(event.Data, &ref); err != nil {
				return nil, err
			}
			if !slices.Contains(sub.DeploymentIDs, ref.DeploymentID) {
				continue
			}
		}
		perm := permission(event.Type)
		key := perm.Name() + ":" + event.ProjectID
		allowed, checked := grants[key]
		if !checked {
			err := e.authz.Require(ctx, p, perm, event.ProjectID)
			if err != nil && !errors.Is(err, security.ErrForbidden) {
				return nil, err
			}
			allowed = err == nil
			grants[key] = allowed
		}
		if allowed {
			if event.Type == "cloud_sync.updated" && len(event.Data) > 0 {
				managerErr := e.authz.Require(ctx, p, security.OrganizationCloudManage, p.OrgID)
				if managerErr != nil && !errors.Is(managerErr, security.ErrForbidden) {
					return nil, managerErr
				}
				if managerErr != nil {
					// Status events also serve replay. Fetch current target details through
					// the grant-checked sync API instead of persisting subscriber authority.
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(event.Data, &payload); err != nil {
						return nil, err
					}
					payload["target_id"] = json.RawMessage(`""`)
					delete(payload, "before")
					delete(payload, "after")
					data, err := json.Marshal(payload)
					if err != nil {
						return nil, err
					}
					event.Data = data
				}
			}
			out = append(out, event)
		}
	}
	return out, nil
}

func (e *Extension) snapshotFor(ctx context.Context, p security.Principal, sub Subscription) (*store.Snapshot, error) {
	out, err := e.store.Snapshot(ctx, p.OrgID, sub.ProjectIDs, sub.DeploymentIDs)
	if err != nil {
		return nil, err
	}
	out.Records, err = e.filter(ctx, p, sub, out.Records)
	return out, err
}

func (e *Extension) snapshot(c forge.Context) error {
	p, sub, err := requestScope(c)
	if err != nil {
		return fail(err)
	}
	if checkErr := e.checkScope(c.Request().Context(), p, sub); checkErr != nil {
		return fail(checkErr)
	}
	out, err := e.snapshotFor(c.Request().Context(), p, sub)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

func (e *Extension) logs(c forge.Context) error {
	p, _ := security.PrincipalFrom(c.Request().Context())
	id := c.Param("id")
	project, err := e.store.ProjectFor(c.Request().Context(), p.OrgID, "deployment", id)
	if err != nil {
		return fail(err)
	}
	if grantErr := e.authz.Require(c.Request().Context(), p, security.DeploymentRead, project); grantErr != nil {
		return fail(grantErr)
	}
	q, err := logQuery(c)
	if err != nil {
		return fail(ErrInvalid)
	}
	out, err := e.store.Logs(c.Request().Context(), p.OrgID, id, q.Cursor, q.Limit)
	if err != nil {
		return fail(err)
	}
	return c.JSON(http.StatusOK, out)
}

type sender func(string, string, any) error

func (e *Extension) validate(ctx context.Context, request *http.Request, p security.Principal, sub Subscription) error {
	fresh, err := e.verify(ctx, request)
	if err != nil {
		return err
	}
	if fresh.UserID != p.UserID || fresh.OrgID != p.OrgID || fresh.KeyID != p.KeyID {
		return security.ErrUnauthenticated
	}
	return e.checkScope(ctx, fresh, sub)
}

// serve watches before reading a snapshot or replay, so the boundary between
// historical and live output cannot miss a commit. Slow writes are bounded by
// Forge's transport deadlines; no per-browser output queue can grow.
func (e *Extension) serve(ctx context.Context, request *http.Request, p security.Principal, sub Subscription, send sender) error {
	wake, unwatch := e.bus.Watch(p.OrgID)
	defer unwatch()
	position := sub.Cursor
	replay := position != ""
	count := 0
	reset := func(gap bool) error {
		if gap {
			if err := send("", forge.EventGap, forge.GapPayload{Reason: "unresolvable"}); err != nil {
				return err
			}
		}
		snapshot, err := e.snapshotFor(ctx, p, sub)
		if err != nil {
			return err
		}
		if err := send(snapshot.Cursor, "realtime.snapshot", snapshot); err != nil {
			return err
		}
		position = snapshot.Cursor
		return nil
	}
	if position == "" {
		if err := reset(false); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := e.validate(ctx, request, p, sub); err != nil {
			return err
		}
		rows, found, err := e.store.Events(ctx, p.OrgID, position, store.MaxReplayEvents+1)
		if err != nil {
			return err
		}
		if !found || len(rows) > store.MaxReplayEvents {
			if resetErr := reset(true); resetErr != nil {
				return resetErr
			}
			rows = nil
		}
		permitted, err := e.filter(ctx, p, sub, rows)
		if err != nil {
			return err
		}
		for _, event := range permitted {
			if err := send(event.ID, event.Type, event.Data); err != nil {
				return err
			}
			count++
		}
		if len(rows) > 0 {
			position = rows[len(rows)-1].ID
		}
		if replay {
			if err := send(position, forge.EventResumed, forge.ResumedPayload{From: sub.Cursor, Count: count}); err != nil {
				return err
			}
			replay = false
		}
		if err := send(position, "realtime.status", Status{Cursor: position, Time: time.Now().UTC()}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-ticker.C:
		}
	}
}

func (e *Extension) events(c forge.Context, stream forge.Stream) error {
	p, sub, err := requestScope(c)
	if err != nil {
		return err
	}
	if id := stream.LastEventID(); id != "" {
		sub.Cursor = id
	}
	return e.serve(stream.Context(), c.Request(), p, sub, stream.SendJSONWithID)
}

func (e *Extension) websocket(c forge.Context, conn forge.Connection) error {
	if bounded, ok := conn.(interface{ SetReadLimit(int64) }); ok {
		bounded.SetReadLimit(16 << 10)
	}
	if bounded, ok := conn.(interface{ SetWriteTimeout(time.Duration) }); ok {
		bounded.SetWriteTimeout(5 * time.Second)
	}
	ctx, cancel := context.WithCancel(conn.Context())
	defer cancel()
	defer conn.Close()
	// The generated client sends its subscription in hello after each open.
	// A bounded first read also prevents idle unauthenticated subscriptions.
	first := make(chan []byte, 1)
	go func() {
		raw, err := conn.Read()
		if err != nil {
			cancel()
			return
		}
		first <- raw
	}()
	var raw []byte
	select {
	case raw = <-first:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return ErrInvalid
	}
	p, _ := security.PrincipalFrom(c.Request().Context())
	sub, err := parseSubscription(p, raw)
	if err != nil {
		return err
	}
	if err := e.checkScope(ctx, p, sub); err != nil {
		return err
	}
	// Only one subscription is accepted per socket. The reader detects close
	// promptly; replacing scope requires an authenticated new connection.
	go func() {
		defer cancel()
		if _, err := conn.Read(); err != nil {
			return
		}
	}()
	return e.serve(ctx, c.Request(), p, sub, func(cursor, event string, data any) error {
		payload, marshalErr := json.Marshal(data)
		if marshalErr != nil {
			return marshalErr
		}
		return conn.WriteJSON(Envelope{Event: event, Data: json.RawMessage(payload), Cursor: cursor, Channel: "/api/v1/realtime/ws"})
	})
}
