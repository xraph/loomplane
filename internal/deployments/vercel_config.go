package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/manifest"
)

// vercelSourceConfig changes only command overrides in the deployment copy.
// The pinned repository remains the authority for services, routes and bindings.
func vercelSourceConfig(data []byte, cfg *planning.ServiceConfig) ([]byte, error) {
	var doc map[string]json.RawMessage
	if len(data) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil || doc == nil {
			return nil, errors.New("vercel.json must contain a JSON object")
		}
	} else {
		doc = map[string]json.RawMessage{}
	}
	grouped := cfg.Vercel != nil && cfg.Vercel.Services
	if !grouped {
		if _, ok := doc["services"]; ok {
			return nil, errors.New("vercel.json defines services; enable Vercel Services in Loomplane to deploy them together")
		}
		if cfg.Commands == nil {
			return data, nil
		}
		setVercelCommands(doc, *cfg.Commands)
		return json.Marshal(doc)
	}
	if cfg.Commands.HasOverrides() {
		return nil, errors.New("Vercel Services requires per-service command overrides")
	}
	var services map[string]map[string]json.RawMessage
	if err := json.Unmarshal(doc["services"], &services); err != nil || len(services) == 0 {
		return nil, errors.New("Vercel Services requires a non-empty services object in vercel.json at the configured project root")
	}
	for _, key := range []string{"experimentalServices", "functions", "installCommand", "buildCommand", "devCommand", "ignoreCommand", "outputDirectory", "framework"} {
		if _, ok := doc[key]; ok {
			return nil, fmt.Errorf("vercel.json: %s is not valid at the top level in Services mode; configure each service", key)
		}
	}
	for name, service := range services {
		var root string
		if service == nil || json.Unmarshal(service["root"], &root) != nil || !fs.ValidPath(strings.TrimSuffix(root, "/")) || strings.Contains(root, "\\") {
			return nil, fmt.Errorf("vercel.json: service %s needs a root directory within the project", name)
		}
	}
	for name, commands := range cfg.Vercel.ServiceCommands {
		service, ok := services[name]
		if !ok {
			return nil, fmt.Errorf("command overrides name Vercel service %q, which is absent from vercel.json", name)
		}
		setVercelCommands(service, commands)
	}
	encoded, err := json.Marshal(services)
	if err != nil {
		return nil, err
	}
	doc["services"] = encoded
	return json.Marshal(doc)
}

func setVercelCommands(doc map[string]json.RawMessage, c manifest.BuildCommands) {
	for key, value := range map[string]*string{"installCommand": c.Install, "buildCommand": c.Build} {
		if value != nil {
			encoded, _ := json.Marshal(*value) //nolint:errcheck // strings always encode
			doc[key] = encoded
		}
	}
}

func vercelConfigPath(cfg *planning.ServiceConfig) string {
	return path.Join(cfg.Source, "vercel.json")
}
