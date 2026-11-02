package cloudinfra

import (
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestFrontendCapacityClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workload  provider.Workload
		execution provider.Execution
		want      bool
	}{
		{"vercel", provider.WebApp, provider.Serverless, true},
		{"vercel", provider.StaticSite, provider.Static, true},
		{"vercel", provider.Worker, provider.Serverless, false},
		{"vercel", provider.HTTPService, provider.Serverless, false},
		{"vercel", provider.WebApp, provider.Container, false},
		{"server", provider.WebApp, provider.Container, false},
	} {
		if got := ManagedFrontend(tc.name, tc.workload, tc.execution); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}
