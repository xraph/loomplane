package main

import (
	"net/http"
	"os"

	"github.com/xraph/forge"
)

func main() {
	app := forge.New(forge.WithAppName("acceptance-api"), forge.WithHTTPAddress(":8080"))
	if err := app.Router().GET("/ready", func(c forge.Context) error {
		status := http.StatusOK
		if os.Getenv("FAIL_READY") == "1" {
			status = http.StatusServiceUnavailable
		}
		return c.JSON(status, map[string]any{"message": "Forge acceptance ready", "secret_present": os.Getenv("API_TOKEN") != ""})
	}); err != nil {
		panic(err)
	}
	if err := app.Run(); err != nil {
		panic(err)
	}
}
