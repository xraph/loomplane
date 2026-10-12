// Command provider-plugin runs the example static host provider. Point
// Loomplane at it:
//
//	STATIC_HOST_TOKEN=... PLUGIN_TOKEN=... go run ./examples/provider-plugin
//
//	# config.yaml
//	providers:
//	  plugins:
//	    - url: http://localhost:9100
//	      token: ${PLUGIN_TOKEN}
//
// then connect "Static host" under Integrations with STATIC_HOST_TOKEN, and
// give a static site `provider: static-host` in loomplane.yaml.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/xraph/loomplane/examples/provider-plugin/statichost"
)

func main() {
	addr := envOr("ADDR", ":9100")
	host := &statichost.Host{
		Dir:     envOr("SITES_DIR", "./sites"),
		BaseURL: envOr("BASE_URL", "http://localhost:9100"),
		Token:   os.Getenv("STATIC_HOST_TOKEN"),
	}
	token := os.Getenv("PLUGIN_TOKEN")
	if host.Token == "" || token == "" {
		fmt.Fprintln(os.Stderr, "provider-plugin: set STATIC_HOST_TOKEN, which connections present, and PLUGIN_TOKEN, which Loomplane presents")
		os.Exit(1)
	}
	srv := &http.Server{Addr: addr, Handler: host.Handler(token), ReadHeaderTimeout: 10 * time.Second}
	fmt.Println("static host provider on", addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "provider-plugin:", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
