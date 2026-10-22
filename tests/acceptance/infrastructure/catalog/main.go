//go:build acceptance

// Command catalog is the image behind the acceptance run's composed catalog
// kind. "catalog migrate" records one run in the provisioned Postgres and
// exits. "catalog serve" refuses to start before a migration ran, answers
// /readyz on :8080 only after a short warm-up, and reports what it saw on
// the control port :9090 under /admin/status.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// warmup is how long /readyz refuses after the server starts, so a ready
// answer can only come from a probe that waited for it.
const warmup = 3 * time.Second

// adminHeader carries the generated admin token. A component channel never
// carries Authorization.
const adminHeader = "X-Lpaccept-Admin-Token"

const schema = `CREATE TABLE IF NOT EXISTS lp_accept_runs (
	id bigserial PRIMARY KEY,
	component text NOT NULL,
	at timestamptz NOT NULL DEFAULT clock_timestamp()
)`

func main() {
	if len(os.Args) != 2 {
		fail(errors.New("usage: catalog migrate|serve"))
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "migrate":
		err = migrate(ctx)
	case "serve":
		err = serve(ctx)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "LPACCEPT_ERROR", err)
	os.Exit(1)
}

// connect retries while the provisioned database starts.
func connect(ctx context.Context) (*pgx.Conn, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		conn, err := pgx.Connect(ctx, url)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to the database: %w", err)
		}
		time.Sleep(time.Second)
	}
}

func migrate(ctx context.Context) error {
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, schema); err != nil {
		return err
	}
	var at time.Time
	if scanErr := conn.QueryRow(ctx, `INSERT INTO lp_accept_runs (component) VALUES ('migrate') RETURNING at`).Scan(&at); scanErr != nil {
		return scanErr
	}
	fmt.Println("LPACCEPT_MIGRATE_DONE", at.UTC().Format(time.RFC3339Nano))
	return nil
}

type status struct {
	Component     string    `json:"component"`
	MigrateRuns   int       `json:"migrate_runs"`
	MigratedAt    time.Time `json:"migrated_at"`
	StartedAt     time.Time `json:"started_at"`
	ReadyRefused  int64     `json:"ready_refused"`
	ReadyAnswered int64     `json:"ready_answered"`
	AdminToken    bool      `json:"admin_token"`
}

func serve(ctx context.Context) error {
	conn, err := connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var runs int
	var migrated *time.Time
	if err = conn.QueryRow(ctx, `SELECT count(*), max(at) FROM lp_accept_runs WHERE component = 'migrate'`).Scan(&runs, &migrated); err != nil {
		return fmt.Errorf("read the migration record: %w", err)
	}
	if runs == 0 || migrated == nil {
		return errors.New("the migrate job has not run")
	}
	var started time.Time
	if scanErr := conn.QueryRow(ctx, `INSERT INTO lp_accept_runs (component) VALUES ('api') RETURNING at`).Scan(&started); scanErr != nil {
		return scanErr
	}
	token := os.Getenv("ADMIN_TOKEN")
	if token == "" {
		return errors.New("ADMIN_TOKEN is not set")
	}
	var refused, answered atomic.Int64
	// One connection serves every admin request, so they take turns.
	var mu sync.Mutex
	ready := time.Now().Add(warmup)
	public := http.NewServeMux()
	public.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if time.Now().Before(ready) {
			refused.Add(1)
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		answered.Add(1)
		_, _ = fmt.Fprintln(w, "ready")
	})
	admin := http.NewServeMux()
	admin.HandleFunc("/admin/status", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(adminHeader)), []byte(token)) != 1 {
			http.Error(w, "admin token required", http.StatusUnauthorized)
			return
		}
		out := status{Component: "api", StartedAt: started.UTC(), ReadyRefused: refused.Load(), ReadyAnswered: answered.Load(), AdminToken: true}
		var last *time.Time
		mu.Lock()
		err := conn.QueryRow(r.Context(), `SELECT count(*), max(at) FROM lp_accept_runs WHERE component = 'migrate'`).Scan(&out.MigrateRuns, &last)
		mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if last != nil {
			out.MigratedAt = last.UTC()
		}
		w.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(w).Encode(out); encodeErr != nil {
			fmt.Fprintln(os.Stderr, "LPACCEPT_ERROR", encodeErr)
		}
	})
	fmt.Println("LPACCEPT_SERVE_STARTED", started.UTC().Format(time.RFC3339Nano))
	errs := make(chan error, 2)
	for _, s := range []*http.Server{
		{Addr: ":8080", Handler: public, ReadHeaderTimeout: 10 * time.Second},
		{Addr: ":9090", Handler: admin, ReadHeaderTimeout: 10 * time.Second},
	} {
		go func() { errs <- s.ListenAndServe() }()
	}
	return <-errs
}
