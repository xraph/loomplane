//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platform"
)

// replicas is how many Loomplane apps boot at once: the Kubernetes chart
// runs two API pods and a rollout briefly runs a third.
const replicas = 3

// API replicas that start together on a fresh database all come up, and
// what they seed on a first start exists once. Seeding looks for a row
// before it creates one, so without the startup lock two replicas both
// found nothing: one failed on the duplicate, and the SMTP provider, which
// has no unique constraint, ended up twice.
func TestPlatform_replicasStartTogether(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.Notifications.SMTPHost = "smtp.invalid"
	cfg.Notifications.From = "loomplane@example.com"
	cfg.Dashboard.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dashboard.Dir, "index.html"), []byte("<div id=root>loomplane</div>"), 0o600); err != nil {
		t.Fatal(err)
	}

	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	type stopper interface{ Stop(context.Context) error }
	var (
		mu      sync.Mutex
		started []stopper
		errs    []error
		wg      sync.WaitGroup
		begin   = make(chan struct{})
	)
	for range replicas {
		wg.Go(func() {
			app, err := platform.New(cfg)
			if err == nil {
				<-begin
				err = app.Start(lifetime)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			started = append(started, app)
		})
	}
	close(begin)
	wg.Wait()
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), startTimeout)
		defer stop()
		for _, app := range started {
			if err := app.Stop(ctx); err != nil {
				t.Errorf("stop app: %v", err)
			}
		}
	})
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("%d of %d replicas failed to start: %v", len(errs), replicas, err)
	}

	ctx := t.Context()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for query, what := range map[string]string{
		`SELECT count(*) FROM authsome_apps`:                                  "authsome apps",
		`SELECT count(*) FROM herald_providers WHERE name = 'loomplane-smtp'`: "SMTP providers",
	} {
		var n int
		if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d replicas seeded %d %s, want 1", replicas, n, what)
		}
	}
}
