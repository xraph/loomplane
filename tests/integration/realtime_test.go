//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
	live "github.com/xraph/loomplane/internal/realtime"
)

func TestRealtime_slowWebSocketReleasesItsSubscription(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c, org := realtimeUser(t, cfg, r)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Large snapshot"}, &project)
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_projects SET description=$2 WHERE id=$1`, project.ID, strings.Repeat("snapshot", 256<<10)); err != nil {
		t.Fatal(err)
	}
	subscribers, err := forge.InjectType[live.SubscriptionCount](r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}
	conn := dialRealtime(t, r, http.Header{"Authorization": {"Bearer " + c.token}})
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err = tcp.SetReadBuffer(1024); err != nil {
			t.Fatal(err)
		}
	}
	if err = wsutil.WriteClientText(conn, []byte(`{"op":"subscribe","org_id":"`+org+`"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if subscribers() != 1 {
		t.Fatal("slow subscriber never opened")
	}
	deadline = time.Now().Add(8 * time.Second)
	for subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if subscribers() != 0 {
		t.Fatal("slow consumer left an unbounded pending subscription")
	}
}

func realtimeUser(t *testing.T, cfg config.Config, r *running) (*client, string) {
	t.Helper()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "stream@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Streams", "slug": "streams"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	return c, org.ID
}

func dialRealtime(t *testing.T, r *running, headers http.Header) net.Conn {
	t.Helper()
	conn, _, _, err := (ws.Dialer{Header: ws.HandshakeHeaderHTTP(headers), Timeout: 5 * time.Second}).Dial(t.Context(), strings.Replace(r.srv.URL, "http://", "ws://", 1)+"/api/v1/realtime/ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readRealtime(t *testing.T, conn net.Conn) (string, string, json.RawMessage) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	raw, _, err := wsutil.ReadServerData(conn)
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Event, Cursor string
		Data          json.RawMessage
	}
	if err = json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	return frame.Event, frame.Cursor, frame.Data
}

func TestRealtime_cookieWebSocketRejectsOriginAndClosesAfterSignout(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c, org := realtimeUser(t, cfg, r)
	headers := http.Header{"Cookie": {cfg.Identity.SessionCookie + "=" + c.token}, "Origin": {"https://foreign.invalid"}}
	_, _, _, err := (ws.Dialer{Header: ws.HandshakeHeaderHTTP(headers)}).Dial(t.Context(), strings.Replace(r.srv.URL, "http://", "ws://", 1)+"/api/v1/realtime/ws")
	if err == nil {
		t.Fatal("foreign origin opened a cookie WebSocket")
	}
	headers.Set("Origin", r.srv.URL)
	conn := dialRealtime(t, r, headers)
	if err = wsutil.WriteClientText(conn, []byte(`{"op":"subscribe","org_id":"`+org+`"}`)); err != nil {
		t.Fatal(err)
	}
	event, cursor, data := readRealtime(t, conn)
	if event != "realtime.snapshot" || cursor == "" || !json.Valid(data) {
		t.Fatalf("snapshot: %s %s %s", event, cursor, data)
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/signout", nil, nil)
	if err = conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		_, _, err = wsutil.ReadServerData(conn)
		if err == nil {
			continue
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("revoked cookie left the stream open")
		}
		break
	}
}

func TestRealtime_apiKeyResumeGapAndRevocation(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	defer r.stop()
	c, org := realtimeUser(t, cfg, r)
	var key keyBody
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "stream", "access": "read"}, &key)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Stream project"}, &project)
	db, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_event_cursors SET floor_seq=next_seq WHERE org_id=$1`, org); err != nil {
		t.Fatal(err)
	}
	conn := dialRealtime(t, r, http.Header{"Authorization": {"Bearer " + key.Secret}})
	if err = wsutil.WriteClientText(conn, []byte(`{"op":"subscribe","org_id":"`+org+`","cursor":"`+org+`:0"}`)); err != nil {
		t.Fatal(err)
	}
	event, _, _ := readRealtime(t, conn)
	if event != "forge.gap" {
		t.Fatalf("missing gap: %s", event)
	}
	event, _, data := readRealtime(t, conn)
	if event != "realtime.snapshot" || !strings.Contains(string(data), project.ID) {
		t.Fatalf("snapshot recovery: %s %s", event, data)
	}
	c.must(http.StatusNoContent, http.MethodDelete, "/api/v1/api-keys/"+key.Key.ID, nil, nil)
	if err = conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		_, _, err = wsutil.ReadServerData(conn)
		if err == nil {
			continue
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("revoked API key left stream open")
		}
		break
	}
}

func TestRealtime_authenticatedSSEReplaysTheCommittedCursor(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/realtime/snapshot", nil, nil)
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "live@example.com", "password": "Correct-horse-battery-9"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Live", "slug": "live"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	var project struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Live project"}, &project)
	var snapshot struct {
		Cursor string `json:"cursor"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/realtime/snapshot?org_id="+org.ID, nil, &snapshot)
	if snapshot.Cursor == "" {
		t.Fatal("snapshot has no atomic high-water cursor")
	}
	c.must(http.StatusConflict, http.MethodGet, "/api/v1/realtime/snapshot?org_id=other_org", nil, nil)
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err = conn.Exec(t.Context(), `UPDATE loomplane_projects SET name='Streamed name',updated_at=now() WHERE id=$1`, project.ID); err != nil {
		t.Fatal(err)
	}
	wsConn := dialRealtime(t, r, http.Header{"Authorization": {"Bearer " + c.token}})
	defer wsConn.Close()
	if err = wsutil.WriteClientText(wsConn, []byte(`{"op":"subscribe","org_id":"`+org.ID+`","cursor":"`+snapshot.Cursor+`"}`)); err != nil {
		t.Fatal(err)
	}
	var wsData json.RawMessage
	for {
		wsEvent, _, data := readRealtime(t, wsConn)
		if wsEvent == "project.updated" && strings.Contains(string(data), "Streamed name") {
			wsData = data
			break
		}
		if wsEvent == "forge.resumed" {
			t.Fatal("WebSocket replay omitted the committed change")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+"/api/v1/realtime/events?org_id="+org.ID, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Last-Event-ID", snapshot.Cursor)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream status: %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	event, data, id := "", "", ""
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case line == "":
			if event == "project.updated" {
				var entity struct{ ID, Name string }
				if err = json.Unmarshal([]byte(data), &entity); err != nil {
					t.Fatal(err)
				}
				if entity.ID == project.ID && entity.Name == "Streamed name" {
					found = true
					var compact bytes.Buffer
					if err = json.Compact(&compact, []byte(data)); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(compact.Bytes(), wsData) {
						t.Fatalf("WS/SSE named payloads differ: %s %s", wsData, data)
					}
					if id == "" || id == snapshot.Cursor {
						t.Fatal("live state did not retain its producer cursor")
					}
				}
			}
			if event == "forge.resumed" {
				if !found {
					t.Fatal("resume completed before delivering the committed state")
				}
				return
			}
			event, data, id = "", "", ""
		}
	}
	t.Fatalf("stream ended before resume: %v", scanner.Err())
}
