package manifests

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

type editStore struct {
	Store
	current *Revision
	reads   int
	writes  int
}

func (s *editStore) CurrentRevision(context.Context, string, string) (*Revision, error) {
	s.reads++
	if s.reads == 1 {
		return &Revision{Revision: 1, YAML: "version: 1\nservices:\n  api:\n    source: go:cmd/api\n    provider: cloud-run\n"}, nil
	}
	return s.current, nil
}
func (s *editStore) CreateRevision(_ context.Context, r *Revision) error {
	s.writes++
	s.current = r
	return nil
}

type editAuth struct{}

func (editAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type editProjects struct{}

func (editProjects) GetProject(context.Context, string, string) (*projects.Project, error) {
	return &projects.Project{}, nil
}

func TestEdit_refusesToOverwriteANewerRevision(t *testing.T) {
	store := &editStore{current: &Revision{Revision: 2, YAML: "# Saved by another editor\nversion: 1\n"}}
	svc := NewService(store, editProjects{}, editAuth{}, nil)
	provider := "ecs-fargate"
	_, _, err := svc.Edit(context.Background(), security.Principal{}, "p", EditInput{Placement: &manifest.Placement{
		Section: "services", Name: "api", Environment: "preview", Provider: &provider,
	}})
	if !errors.Is(err, ErrConflict) || store.writes != 0 || store.current.Revision != 2 {
		t.Fatalf("overwrote another editor's revision: err=%v writes=%d revision=%d", err, store.writes, store.current.Revision)
	}
}
