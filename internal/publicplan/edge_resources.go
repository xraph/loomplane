package publicplan

import (
	"encoding/json"
	"slices"

	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func projectWorkerSettings(v *provider.WorkerSettings) *provider.WorkerSettings {
	if v == nil {
		return nil
	}
	out := *v
	out.CompatibilityFlags = slices.Clone(v.CompatibilityFlags)
	out.DurableObjects = slices.Clone(v.DurableObjects)
	return &out
}
func edgeResourceSettings(in map[string]string) map[string]string {
	var out map[string]string
	for _, key := range []string{"location", "jurisdiction", "dimensions", "metric", "origin_endpoint", "application_endpoint", "ca_certificate_id", "project_id", "integration_configuration_id", "integration_product_id", "billing_plan_id", "output_keys", "access"} {
		if value := in[key]; value != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[key] = value
		}
	}
	if raw := in["metadata"]; raw != "" {
		if metadata, err := vercel.ParseProvisioningMetadata(raw); err == nil {
			if canonical, encodeErr := json.Marshal(metadata); encodeErr == nil {
				if out == nil {
					out = map[string]string{}
				}
				out["metadata"] = string(canonical)
			}
		}
	}
	return out
}
func edgeEndpointPins(in map[string]planning.EndpointPin) map[string]planning.EndpointPin {
	var out map[string]planning.EndpointPin
	for _, key := range []string{"origin_endpoint", "application_endpoint"} {
		if value, ok := in[key]; ok {
			if out == nil {
				out = map[string]planning.EndpointPin{}
			}
			out[key] = value
		}
	}
	return out
}
