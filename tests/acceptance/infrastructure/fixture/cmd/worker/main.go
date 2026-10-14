package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xraph/forge"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app := forge.New(forge.WithAppName("acceptance-worker"))
	if err := app.Start(ctx); err != nil {
		panic(err)
	}
	defer func() {
		if err := app.Stop(context.Background()); err != nil {
			panic(err)
		}
	}()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if address := os.Getenv("API_URL"); address != "" {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/ready", http.NoBody) //nolint:gosec // The fixture probes the private dependency URL supplied by Loomplane.
			if err != nil {
				panic(err)
			}
			response, err := client.Do(request) //nolint:gosec // This private dependency request is the acceptance assertion.
			if err == nil {
				if response.StatusCode == http.StatusOK {
					fmt.Println("PRIVATE_DEPENDENCY_READY")
				}
				_ = response.Body.Close()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
