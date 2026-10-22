//go:build integration && neon

package dbgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// neonDir holds the pinned @neondatabase/serverless install and its scripts.
var neonDir = filepath.Join("..", "..", "internal", "dbgateway", "plane", "testdata", "neon")

// neonCall is what e2e.mjs saw from one call: its result, or the SQLSTATE
// and message it failed with.
type neonCall struct {
	One      *int   `json:"one"`
	N        *int   `json:"n"`
	Accepted bool   `json:"accepted"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// TestE2ENeonDriver runs @neondatabase/serverless 1.2.0, configured as the
// docs say, against the gateway built from dbgateway.Source(): a Pool query
// over the WebSocket, where the driver pipelines its password behind the
// startup packet without waiting for the cleartext request, a neon()
// query over HTTP, and a wrong password refused with 28P01 over both.
func TestE2ENeonDriver(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	if _, err = os.Stat(filepath.Join(neonDir, "node_modules", "@neondatabase", "serverless")); err != nil {
		t.Skip("run `npm ci --prefix internal/dbgateway/plane/testdata/neon` first")
	}
	g := newE2ERig(t, 0)
	gw := g.start(filepath.Join(t.TempDir(), "state"))

	// The shape resourceAddress hands an app: the logical host on port
	// 5432, with the backend's sslmode=require carried over, so the test
	// proves the driver copes with exactly that.
	dsn := func(password string) string {
		u := url.URL{Scheme: "postgres", User: url.UserPassword(g.user, password), Host: e2eHost + ":5432", Path: "/" + g.database, RawQuery: "sslmode=require"}
		return u.String()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "e2e.mjs")
	cmd.Dir = neonDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GATEWAY=127.0.0.1:%d", gw.port),
		"DSN="+dsn(g.password),
		"BAD_DSN="+dsn(g.password+"-wrong"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err = cmd.Run(); err != nil {
		t.Fatalf("node e2e.mjs: %v\n%s", err, stderr.String())
	}
	var got map[string]neonCall
	if err = json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("e2e.mjs printed %q: %v", stdout.String(), err)
	}
	t.Logf("e2e.mjs: %s", bytes.TrimSpace(stdout.Bytes()))

	if c := got["ws"]; c.One == nil || *c.One != 1 {
		t.Errorf("Pool.query('SELECT 1') over the WebSocket: %+v", c)
	}
	if c := got["http"]; c.N == nil || *c.N != 7 {
		t.Errorf("neon().query('SELECT $1::int AS n', [7]) over HTTP: %+v", c)
	}
	for _, k := range []string{"wsBadPassword", "httpBadPassword"} {
		if c := got[k]; c.Accepted || c.Code != "28P01" {
			t.Errorf("%s: %+v, want SQLSTATE 28P01", k, c)
		}
	}
}
