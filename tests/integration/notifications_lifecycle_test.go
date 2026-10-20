//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
)

func TestNotificationsLifecycleStopsDeliveryWorkers(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	r := start(t, cfg)
	r.srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- r.app.Stop(ctx) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("notification workers did not stop within the application shutdown deadline")
	}
}
