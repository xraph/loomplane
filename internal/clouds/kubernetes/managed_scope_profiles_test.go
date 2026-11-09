package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManagedScopeProfileFoundation(t *testing.T) {
	s := ManagedScope{Org: "org", Project: "project", Environment: "env", Service: "api", Plan: "plan", Deployment: "deploy", Action: "service:api", Connection: "connection", Instance: "instance", RuntimeRef: "org/env/api/runtime_one", RuntimeRevision: "deploy/service:api", Namespace: "apps", NamespaceUID: "namespace", ClusterCA: strings.Repeat("a", 64), Epoch: 1}
	s.Release = WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision)
	if !legacyManagedScopeValid(s) || !managedScopeValid(s) || hasGroupScope(s) || hasLifecycleScope(s) {
		t.Fatal("original scope was refused")
	}
	for _, tc := range []struct {
		name   string
		change func(*ManagedScope)
		group  bool
	}{
		{"group", func(s *ManagedScope) { s.GroupID = "group" }, true},
		{"member", func(s *ManagedScope) { s.MemberID = "member" }, true},
		{"generation", func(s *ManagedScope) { s.GroupGeneration = 1 }, true},
		{"backend-generation", func(s *ManagedScope) { s.BackendGeneration = 1 }, true},
		{"operation", func(s *ManagedScope) { s.GroupOperation = "prepare" }, true},
		{"lifecycle-run", func(s *ManagedScope) { s.LifecycleRunID = "run" }, false},
		{"lifecycle-job", func(s *ManagedScope) { s.LifecycleJobName = "job" }, false},
		{"lifecycle-digest", func(s *ManagedScope) { s.LifecycleDigest = "digest" }, false},
		{"lifecycle-spec", func(s *ManagedScope) { s.LifecycleSpecDigest = "spec" }, false},
		{"lifecycle-secret", func(s *ManagedScope) { s.LifecycleSecretDigest = "secret" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := s
			tc.change(&candidate)
			if legacyManagedScopeValid(candidate) || managedScopeValid(candidate) || hasGroupScope(candidate) != tc.group || hasLifecycleScope(candidate) == tc.group {
				t.Fatal("partial new profile inherited legacy authority")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*ManagedScope)
	}{
		{"missing-org", func(s *ManagedScope) { s.Org = "" }},
		{"missing-project", func(s *ManagedScope) { s.Project = "" }},
		{"missing-environment", func(s *ManagedScope) { s.Environment = "" }},
		{"missing-service", func(s *ManagedScope) { s.Service = "" }},
		{"missing-plan", func(s *ManagedScope) { s.Plan = "" }},
		{"missing-deployment", func(s *ManagedScope) { s.Deployment = "" }},
		{"missing-action", func(s *ManagedScope) { s.Action = "" }},
		{"missing-connection", func(s *ManagedScope) { s.Connection = "" }},
		{"missing-instance", func(s *ManagedScope) { s.Instance = "" }},
		{"missing-namespace", func(s *ManagedScope) { s.Namespace = "" }},
		{"missing-namespace-uid", func(s *ManagedScope) { s.NamespaceUID = "" }},
		{"zero-epoch", func(s *ManagedScope) { s.Epoch = 0 }},
		{"negative-epoch", func(s *ManagedScope) { s.Epoch = -1 }},
		{"release-mismatch", func(s *ManagedScope) { s.Release = "other" }},
		{"runtime-prefix", func(s *ManagedScope) {
			s.RuntimeRef = "other/env/api/runtime_one"
			s.Release = WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision)
		}},
		{"revision-link", func(s *ManagedScope) {
			s.RuntimeRevision = "other/service:api"
			s.Release = WorkloadReleaseID(s.RuntimeRef, s.RuntimeRevision)
		}},
		{"ca-length", func(s *ManagedScope) { s.ClusterCA = "short" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := s
			tc.change(&candidate)
			if legacyManagedScopeValid(candidate) || managedScopeValid(candidate) {
				t.Fatal("malformed legacy authority accepted")
			}
		})
	}
	mixed := s
	mixed.GroupID = "group"
	mixed.LifecycleRunID = "run"
	if legacyManagedScopeValid(mixed) || managedScopeValid(mixed) || !hasGroupScope(mixed) || !hasLifecycleScope(mixed) {
		t.Fatal("mixed profiles were accepted")
	}
	raw, err := json.Marshal(ManagedScope{})
	if err != nil {
		t.Fatal(err)
	}
	const historical = `{"Org":"","Project":"","Environment":"","Service":"","Plan":"","Deployment":"","Action":"","Connection":"","Instance":"","RuntimeRef":"","RuntimeRevision":"","Release":"","Namespace":"","NamespaceUID":"","ClusterCA":"","Epoch":0}`
	if string(raw) != historical {
		t.Fatalf("legacy authority JSON changed: %s", raw)
	}
}
