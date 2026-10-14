//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws/wsutil"

	"github.com/xraph/loomplane/internal/realtime"
)

type infrastructureFrame struct {
	Event  string          `json:"event"`
	Cursor string          `json:"cursor"`
	Data   json.RawMessage `json:"data"`
}

type infrastructureFeed struct {
	mu     sync.Mutex
	frames []infrastructureFrame
	err    error
	close  func()
	done   chan struct{}
}

func (f *infrastructureFeed) record(frame infrastructureFrame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, frame)
}

func (f *infrastructureFeed) marker(deployment string) (realtime.Log, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, frame := range f.frames {
		var line realtime.Log
		if frame.Event == "build.log" && json.Unmarshal(frame.Data, &line) == nil && line.DeploymentID == deployment && strings.Contains(line.Message, "LOOMPLANE_BUILD_MARKER") {
			return line, frame.Cursor, true
		}
	}
	return realtime.Log{}, "", false
}

func infrastructureWS(t *testing.T, r *running, c *client, org, cursor, deployment string) *infrastructureFeed {
	t.Helper()
	conn := dialRealtime(t, r, http.Header{"Authorization": {"Bearer " + c.token}})
	f := &infrastructureFeed{done: make(chan struct{}), close: func() { _ = conn.Close() }}
	subscribe, err := json.Marshal(map[string]any{"op": "subscribe", "org_id": org, "cursor": cursor, "deployment_ids": []string{deployment}})
	if err != nil {
		t.Fatal(err)
	}
	if err = wsutil.WriteClientText(conn, subscribe); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(f.done)
		for {
			raw, _, readErr := wsutil.ReadServerData(conn)
			if readErr != nil {
				f.mu.Lock()
				f.err = readErr
				f.mu.Unlock()
				return
			}
			var frame infrastructureFrame
			if decodeErr := json.Unmarshal(raw, &frame); decodeErr != nil {
				f.mu.Lock()
				f.err = decodeErr
				f.mu.Unlock()
				return
			}
			f.record(frame)
		}
	}()
	t.Cleanup(func() { f.close(); <-f.done })
	return f
}

func infrastructureSSE(t *testing.T, c *client, org, cursor, deployment string) *infrastructureFeed {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/realtime/events?org_id="+org+"&deployment_ids="+deployment, http.NoBody)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	res, err := http.DefaultClient.Do(req) //nolint:bodyclose // The feed owns and closes the streaming body in cleanup.
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		cancel()
		_ = res.Body.Close()
		t.Fatalf("SSE: %d", res.StatusCode)
	}
	f := &infrastructureFeed{done: make(chan struct{}), close: func() { cancel(); _ = res.Body.Close() }}
	go func() {
		defer close(f.done)
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		frame := infrastructureFrame{}
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				frame.Cursor = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				frame.Data = json.RawMessage(strings.TrimPrefix(line, "data: "))
			case line == "":
				if frame.Event != "" {
					f.record(frame)
				}
				frame = infrastructureFrame{}
			}
		}
		f.mu.Lock()
		f.err = scanner.Err()
		f.mu.Unlock()
	}()
	t.Cleanup(func() { f.close(); <-f.done })
	return f
}

func waitInfrastructureMarker(t *testing.T, c *client, feed *infrastructureFeed, id string) (realtime.Log, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if line, cursor, found := feed.marker(id); found {
			var deployment deploymentBody
			c.must(http.StatusOK, http.MethodGet, "/api/v1/deployments/"+id, nil, &deployment)
			if deployment.Status != "running" {
				t.Fatalf("build marker arrived only after completion: %s", deployment.Status)
			}
			return line, cursor
		}
		feed.mu.Lock()
		err := feed.err
		feed.mu.Unlock()
		if err != nil {
			t.Fatal(fmt.Errorf("build stream: %w", err))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no real build marker arrived before completion")
	return realtime.Log{}, ""
}
