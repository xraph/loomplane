package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/recipes"
)

func probeIntegrationAccess(ctx context.Context, client *http.Client, dial DialFunc, a APIAccess) error {
	names := make([]string, 0, len(a.Values))
	for name := range a.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	checked := map[string]bool{}
	count := 0
	for _, name := range names {
		raw := a.Values[name]
		if checked[raw] {
			continue
		}
		engine := ""
		switch {
		case outputSuffix(name, "DATABASE_URL") || outputSuffix(name, "POSTGRES_URL"):
			engine = EnginePostgres
		case outputSuffix(name, "REDIS_URL") || outputSuffix(name, "KV_URL"):
			engine = EngineRedis
		}
		if engine == "" {
			continue
		}
		endpoint := &Endpoint{Engine: engine}
		if err := parseDSN(endpoint, raw); err != nil || endpoint.TLS == TLSDisable {
			return errors.New("marketplace database access requires a supported TLS connection URL")
		}
		var prober Prober
		if engine == EnginePostgres {
			// Verify certificates during the check even when the provider's client URL says require.
			parsed, err := url.Parse(raw)
			if err != nil {
				return invalidAPIAccess()
			}
			query := parsed.Query()
			query.Set("sslmode", TLSVerifyFull)
			parsed.RawQuery = query.Encode()
			raw = parsed.String()
			prober = PostgresProber{}
		} else {
			prober = RedisProber{}
		}
		report, err := prober.Probe(ctx, dial, raw, recipes.Requirement{Engine: engine, TLS: []string{TLSVerifyFull}})
		if err != nil || report == nil || !report.Result().OK {
			return errors.New("the Marketplace backing database refused its application connection check")
		}
		checked[a.Values[name]] = true
		count++
	}
	for _, name := range names {
		suffix := ""
		switch {
		case outputSuffix(name, "KV_REST_API_URL"):
			suffix = "KV_REST_API_URL"
		case outputSuffix(name, "UPSTASH_REDIS_REST_URL"):
			suffix = "UPSTASH_REDIS_REST_URL"
		}
		if suffix == "" {
			continue
		}
		base := a.Values[name]
		if checked[base] {
			continue
		}
		tokenKey := strings.TrimSuffix(name, "URL") + "TOKEN"
		token := a.Values[tokenKey]
		if err := probeUpstashREST(ctx, client, base, token); err != nil {
			return err
		}
		checked[base] = true
		count++
	}
	if count == 0 {
		return errors.New("the Marketplace application outputs have no supported database or Upstash connection check")
	}
	return nil
}
func outputSuffix(name, suffix string) bool {
	return name == suffix || strings.HasSuffix(name, "_"+suffix)
}
func probeUpstashREST(ctx context.Context, client *http.Client, raw, token string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" && endpoint.Port() != "443" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" || !strings.HasSuffix(endpoint.Hostname(), ".upstash.io") || strings.Count(endpoint.Hostname(), ".") != 2 || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("the Upstash application connection is not a supported scoped HTTPS endpoint")
	}
	endpoint.Path = "/ping"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), http.NoBody)
	if err != nil {
		return invalidAPIAccess()
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("the Upstash application ping failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("the Upstash application ping was refused")
	}
	body, err := readBucketBody(response.Body)
	if err != nil {
		return err
	}
	var result struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(body, &result) != nil || result.Result != "PONG" {
		return errors.New("the Upstash application ping could not be verified")
	}
	return nil
}
