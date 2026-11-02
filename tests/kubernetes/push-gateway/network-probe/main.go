package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func probe(ctx context.Context, gatewayURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(gatewayURL, "/")+"/v1/send", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("build readiness request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("gateway readiness request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("gateway readiness: expected HTTP 401, got HTTP %d", response.StatusCode)
	}
	return nil
}

func main() {
	const fixtureGateway = "https://push.loomplane.test"
	if os.Getenv("LOOMPLANE_PUSH_GATEWAY_URL") != fixtureGateway {
		fmt.Fprintln(os.Stderr, "gateway readiness requires the fixture gateway URL")
		os.Exit(1)
	}
	if err := probe(context.Background(), fixtureGateway); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
