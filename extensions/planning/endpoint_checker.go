package planning

import "github.com/xraph/loomplane/internal/catalogconnect"

func newEndpointChecker(endpoints *catalogconnect.Service) *catalogconnect.Checker {
	checker := &catalogconnect.Checker{Endpoints: endpoints, Probers: map[string]catalogconnect.Prober{
		catalogconnect.EnginePostgres: catalogconnect.PostgresProber{}, catalogconnect.EngineRedis: catalogconnect.RedisProber{},
		catalogconnect.EngineMySQL:         catalogconnect.MySQLProber{},
		catalogconnect.EngineElasticsearch: catalogconnect.SearchProber{}, catalogconnect.EngineOpenSearch: catalogconnect.SearchProber{},
		catalogconnect.EngineS3: catalogconnect.BucketProber{}, catalogconnect.EngineGCS: catalogconnect.BucketProber{},
	}}
	checker.Probers[catalogconnect.EngineR2] = catalogconnect.BucketProber{}
	for _, engine := range []string{"cloudflare-d1", "cloudflare-kv", "cloudflare-r2", "cloudflare-queue", "cloudflare-vectorize", "cloudflare-hyperdrive", "cloudflare-durable-object", "vercel-blob", "vercel-global-config", "vercel-integration"} {
		checker.Probers[engine] = catalogconnect.APIProber{}
	}
	return checker
}
