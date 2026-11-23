package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
)

// MethodRunner is a runner pool's connection. Loomplane writes its details
// from what the pool's runners showed when they connected, so it has no
// credential, and nobody creates one through Create.
const MethodRunner = "runner"

// runnerCredential is the marker a runner pool's checks and builds get in
// place of a credential. Builds reach runners through their node streams.
var runnerCredential = []byte(`{"runner_pool":true}`)

// RunnerChecker accepts a runner pool's details as Loomplane recorded them.
// A pool is checked when its runners connect, not by asking a cloud, so a
// recheck reports what its runners last showed: ready once one passed its
// KVM and microVM check.
type RunnerChecker struct{}

// Parse implements Checker.
func (RunnerChecker) Parse(_ []byte, settings map[string]string) (Check, error) {
	if settings["pool_id"] == "" {
		return Check{}, fmt.Errorf("%w: a runner pool connection names its pool", ErrInvalid)
	}
	return Check{Account: settings["pool_id"], Details: maps.Clone(settings)}, nil
}

// Check implements Checker.
func (c RunnerChecker) Check(_ context.Context, credential []byte, settings map[string]string) (Check, error) {
	res, err := c.Parse(credential, settings)
	if err == nil && settings["build_isolation"] != "ready" {
		res.Missing = []string{"runner:online"}
	}
	return res, err
}

// PublishRunnerPool creates a runner pool's connection, or writes what its
// runners showed onto the one it has. Only the runner pool service calls
// it, with details it read from the runners themselves.
func (s *Service) PublishRunnerPool(ctx context.Context, orgID, id, name, createdBy string, details map[string]string) (*Connection, error) {
	check, err := RunnerChecker{}.Check(ctx, runnerCredential, details)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	status := StatusReady
	if len(check.Missing) > 0 {
		status = StatusIncomplete
	}
	current, err := s.store.GetConnection(ctx, orgID, id)
	switch {
	case errors.Is(err, ErrNotFound):
		c := &Connection{ID: id, OrgID: orgID, Cloud: Runner, Name: name, Method: MethodRunner, Account: check.Account, Details: check.Details, Status: status, Missing: check.Missing, CheckedAt: &now, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}
		if xerr := s.store.CreateConnection(ctx, c); xerr != nil {
			return nil, xerr
		}
		return c, nil
	case err != nil:
		return nil, err
	case current.Cloud != Runner || current.Method != MethodRunner:
		return nil, fmt.Errorf("%w: %s isn't a runner pool's connection", ErrInvalid, id)
	}
	current.Account, current.Details, current.Status, current.Missing, current.CheckedAt, current.UpdatedAt = check.Account, check.Details, status, check.Missing, &now, now
	current.Message = ""
	if err := s.store.UpdateConnection(ctx, current); err != nil {
		return nil, err
	}
	return current, nil
}

// DeleteRunnerPool removes a runner pool's connection. Only the runner pool
// service calls it, once the pool has no runners.
func (s *Service) DeleteRunnerPool(ctx context.Context, orgID, id string) error {
	c, err := s.store.GetConnection(ctx, orgID, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.Cloud != Runner || c.Method != MethodRunner {
		return fmt.Errorf("%w: %s isn't a runner pool's connection", ErrInvalid, id)
	}
	return s.store.DeleteConnection(ctx, orgID, id)
}
