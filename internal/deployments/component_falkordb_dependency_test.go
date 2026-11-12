package deployments

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

type falkorDependencyProbe func(context.Context, string, recipes.Requirement, catalogconnect.DialFunc) (*catalogconnect.Report, error)

func (p falkorDependencyProbe) CheckDSN(ctx context.Context, raw string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	return p(ctx, raw, req, dial)
}

func (falkorDependencyProbe) Check(context.Context, string, string, recipes.Requirement, catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	return nil, errors.New("the checker must use the already resolved access")
}

func TestFalkorDependencyChecksResolvedAccessFromDeploymentTarget(t *testing.T) {
	for _, tc := range []struct{ name, mode, ca string }{
		{"verified with inline CA", catalogconnect.TLSVerifyFull, searchDependencyCA(t)},
		{"verified with system roots", catalogconnect.TLSVerifyFull, ""},
		{"plaintext", catalogconnect.TLSDisable, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkFalkorDependencyAccess(t, tc.mode, tc.ca)
		})
	}
}

func checkFalkorDependencyAccess(t *testing.T, mode, ca string) {
	t.Helper()
	a := catalogconnect.EngineAccess{Engine: "falkordb", Host: "graph.example", Username: "app", Password: "fixture-workload-password", TLS: mode, CACert: ca}
	raw, err := catalogconnect.EncodeEngineAccess(a)
	if err != nil {
		t.Fatal("fixture could not encode access")
	}
	fields := map[string]string{
		recipes.FieldURL: "graph.example:6379", recipes.FieldPassword: a.Password, recipes.FieldCACert: a.CACert, recipes.FieldTLSMode: a.TLS,
	}
	f := newComponentFixture(t, sampleKinds(t, func(k *recipes.Kind) {
		k.Dependencies[0].External = &recipes.Requirement{Engine: a.Engine, MinVersion: "4.2", TLS: []string{a.TLS}}
		k.Components[1].Env = nil
		for _, field := range []string{recipes.FieldURL, recipes.FieldPassword, recipes.FieldCACert, recipes.FieldTLSMode} {
			k.Components[1].Env = append(k.Components[1].Env, recipes.EnvVar{Name: field, From: &recipes.Ref{Dependency: "postgres", Field: field}})
		}
	}))
	f.target.Endpoints = fakeEndpoints{endpoint: &catalogconnect.Endpoint{ID: "graph-endpoint", OrgID: "org", Name: "graph", Engine: a.Engine, Host: a.Host, Port: 6379, Username: a.Username, TLS: a.TLS, Revision: 3}, dsn: raw}
	dialed, checked := 0, 0
	f.target.endpointDial = func(_ context.Context, org, target string) (catalogconnect.DialFunc, io.Closer, error) {
		if org != "org" || target != "conn" {
			t.Fatal("FalkorDB recheck lost the deployment target")
		}
		return func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "graph.example:6379" {
				t.Fatal("wrong recheck authority")
			}
			dialed++
			return nil, nil
		}, noClose{}, nil
	}
	f.target.Checker = falkorDependencyProbe(func(ctx context.Context, actual string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
		checked++
		if actual != raw || req.Engine != a.Engine || req.MinVersion != "4.2" || len(req.TLS) != 1 || req.TLS[0] != a.TLS || catalogconnect.ConsumerFrom(ctx).Title != "Sample" {
			t.Fatal("FalkorDB recheck lost the requirement or resolved access")
		}
		if _, err := dial(ctx, "tcp", "graph.example:6379"); err != nil {
			return nil, err
		}
		return &catalogconnect.Report{Version: "4.2.2"}, nil
	})
	deps := map[string]planning.DependencyChoice{"postgres": {Mode: planning.DependencyExternal, External: "graph-endpoint"}, "cache": {Mode: planning.DependencyOff}}
	kind := mustKind(t, f.kinds, "sample")
	job := f.componentJob(t, "api", deps)
	run, secret, err := f.target.componentEnv(t.Context(), job, kind)
	if err != nil || dialed != 1 || checked != 1 {
		t.Fatal("FalkorDB fields did not share the target recheck")
	}
	for field, expected := range fields {
		if !secret[field] || run.Env[field] != expected {
			t.Fatal("incorrect or public FalkorDB field")
		}
	}
	reads := f.target.reads(job, kind, "app", deps)
	for _, field := range []string{"output:password", "secret:password", "admin-url"} {
		if value, on, err := reads.dependency(t.Context(), "postgres", field); err == nil || on || value != "" {
			t.Fatal("external private field resolved")
		}
	}
}

func TestFalkorManagedDependencyTLSProfile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		revision    string
		choice      string
		mapped      bool
		change      func(*Job)
		outputs     map[string]string
		wantMode    string
		wantCA      string
		wantModeErr bool
		wantCAErr   bool
	}{
		{name: "attached plaintext revision", revision: "2", choice: planning.DependencyResource, wantMode: catalogconnect.TLSDisable},
		{name: "new plaintext revision", revision: "3", choice: planning.DependencyNew, wantMode: catalogconnect.TLSDisable},
		{
			name: "declared verified mode and system roots", revision: "4", choice: planning.DependencyResource, mapped: true,
			outputs:  map[string]string{"output:tls-mode": catalogconnect.TLSVerifyFull, "output:ca-cert": ""},
			wantMode: catalogconnect.TLSVerifyFull,
		},
		{
			name: "declared verified mode and inline CA", revision: "4", choice: planning.DependencyResource, mapped: true,
			outputs:  map[string]string{"output:tls-mode": catalogconnect.TLSVerifyFull, "output:ca-cert": "fixture CA"},
			wantMode: catalogconnect.TLSVerifyFull, wantCA: "fixture CA",
		},
		{
			name: "missing declared mode and CA", revision: "3", choice: planning.DependencyResource, mapped: true,
			wantModeErr: true, wantCAErr: true,
		},
		{
			name: "empty declared mode", revision: "3", choice: planning.DependencyResource, mapped: true,
			outputs:     map[string]string{"output:tls-mode": "", "output:ca-cert": ""},
			wantModeErr: true,
		},
		{
			name: "unknown profile without metadata", revision: "4", choice: planning.DependencyResource,
			wantModeErr: true, wantCAErr: true,
		},
		{
			name: "missing reviewed resource", revision: "3", choice: planning.DependencyResource,
			change: func(job *Job) { job.Plan.Actions = nil }, wantModeErr: true, wantCAErr: true,
		},
		{
			name: "wrong plan scope", revision: "3", choice: planning.DependencyResource,
			change: func(job *Job) { job.Plan.EnvironmentID = "other" }, wantModeErr: true, wantCAErr: true,
		},
		{
			name: "mismatched image", revision: "3", choice: planning.DependencyResource,
			change: func(job *Job) { job.Plan.Actions[0].Resource.Component.Component.Image = "other" }, wantModeErr: true, wantCAErr: true,
		},
		{
			name: "empty kept connection", revision: "3", choice: planning.DependencyResource,
			outputs: map[string]string{ResourceURL: ""}, wantModeErr: true, wantCAErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, job := falkorManagedTLSFixture(t, tc.revision, tc.mapped)
			if tc.change != nil {
				tc.change(job)
			}
			for key, value := range tc.outputs {
				if err := f.secrets.Put(t.Context(), ResourceKey("org", "env", "graph", key), []byte(value)); err != nil {
					t.Fatal("keep test transport output")
				}
			}
			deps := map[string]planning.DependencyChoice{"graph": {Mode: tc.choice, Resource: "graph", Kind: catalogconnect.EngineFalkorDB}}
			reads := f.target.reads(job, recipes.Kind{}, "app", deps)
			gotMode, modeOn, modeErr := reads.dependency(t.Context(), "graph", recipes.FieldTLSMode)
			if (modeErr != nil) != tc.wantModeErr || modeOn == tc.wantModeErr || gotMode != tc.wantMode {
				t.Fatal("managed dependency resolved the wrong transport policy")
			}
			gotCA, caOn, caErr := reads.dependency(t.Context(), "graph", recipes.FieldCACert)
			if (caErr != nil) != tc.wantCAErr || caOn == tc.wantCAErr || gotCA != tc.wantCA {
				t.Fatal("managed dependency resolved the wrong CA")
			}
		})
	}
}

func falkorManagedTLSFixture(t *testing.T, revision string, mapped bool) (*componentFixture, *Job) {
	t.Helper()
	kinds := recipes.Default()
	if revision == "4" || mapped {
		kind := mustKind(t, kinds, catalogconnect.EngineFalkorDB)
		kind.Releases[0].Revision = revision
		if mapped {
			kind.Service.Connection[recipes.FieldTLSMode] = "tls-mode"
			kind.Service.Connection[recipes.FieldCACert] = "ca-cert"
			kind.Outputs = append(kind.Outputs,
				recipes.Output{Name: "tls-mode", EnvKey: "FALKORDB_TLS_MODE", Template: catalogconnect.TLSVerifyFull},
				recipes.Output{Name: "ca-cert", EnvKey: "FALKORDB_CA_CERT", Template: "fixture CA"},
			)
		}
		kinds = recipes.NewRegistry()
		if err := kinds.Register(kind); err != nil {
			t.Fatal(err)
		}
	}
	_, resolved, err := kinds.ResolveRevision(catalogconnect.EngineFalkorDB, "4.2", revision, "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	f := newComponentFixture(t, kinds)
	job := f.job("app--api", &planning.ResourceConfig{})
	job.Plan.OrgID, job.Plan.ProjectID, job.Plan.EnvironmentID = job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID
	component := resolved.Components[0]
	job.Plan.Actions = []planning.Action{{
		ID: "resource:graph", Subject: planning.ResourceSubject, Name: "graph", Op: planning.Keep, ConnectionID: "conn",
		Resource: &planning.ResourceConfig{
			Provider: "server", Target: "conn", TargetSettings: map[string]string{"architecture": "amd64"}, Kind: resolved.Kind, Version: resolved.Version,
			Component: &planning.ComponentConfig{Kind: resolved.Kind, Resource: "graph", Revision: resolved.Revision, Component: component, Protection: planning.ComponentProtection(component.Component)},
		},
	}}
	if err = f.secrets.Put(t.Context(), ResourceKey("org", "env", "graph", ResourceURL), []byte("graph.example:6379")); err != nil {
		t.Fatal("keep test connection")
	}
	return f, job
}
