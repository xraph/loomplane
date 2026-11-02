package agentops

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/xraph/cortex"

	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
)

// Call is who calls an operation, where, and in which run.
type Call struct {
	// Principal is the agent principal, with the person when it acts for
	// one. Every service call goes as it.
	Principal security.Principal
	// Scope is the run's: always the organization, maybe one project and
	// one of its environments.
	Scope  agentscope.Scope
	TaskID string
	RunID  string
	CallID string
}

// CallFrom reads the Call of a cortex tool invocation. The principal must be
// a security.Principal of kind agent, the scope one agentscope made, and
// both must name the same organization, as must the person the agent acts
// for.
func CallFrom(inv cortex.Invocation) (Call, error) {
	p, ok := inv.Principal.(security.Principal)
	if !ok {
		return Call{}, fmt.Errorf("%w: the call carries no Loomplane principal", security.ErrUnauthenticated)
	}
	if !p.IsAgent() {
		return Call{}, fmt.Errorf("%w: only the agent calls its operations", security.ErrForbidden)
	}
	scope, err := agentscope.FromCortex(inv.Scope)
	if err != nil {
		return Call{}, err
	}
	if p.OrgID != scope.OrgID || p.UserID != security.AgentSubject(scope.OrgID) {
		return Call{}, fmt.Errorf("%w: the agent acts only in its own organization", security.ErrForbidden)
	}
	if person, ok := p.Person(); ok && person.OrgID != scope.OrgID {
		return Call{}, fmt.Errorf("%w: the agent acts only for its own organization's members", security.ErrForbidden)
	}
	return Call{Principal: p, Scope: scope, RunID: inv.RunID.String(), CallID: inv.Call.ID}, nil
}

type taskKey struct{}

// WithTaskID marks ctx as part of the agent task taskID. The run driver
// sets it on the context it runs cortex with, so each call's audit entry
// names the task.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, taskKey{}, taskID)
}

// TaskIDFrom returns the task WithTaskID put on ctx, or "".
func TaskIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(taskKey{}).(string); ok {
		return id
	}
	return ""
}

// callState is what a handler learns as it runs that the audit entry
// needs: the project it acted on.
type callState struct {
	mu        sync.Mutex
	projectID string
}

type callStateKey struct{}

func (s *callState) project() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectID
}

// noteProject records the project the call acts on, for its audit entry.
func noteProject(ctx context.Context, projectID string) {
	if s, ok := ctx.Value(callStateKey{}).(*callState); ok && projectID != "" {
		s.mu.Lock()
		s.projectID = projectID
		s.mu.Unlock()
	}
}

// errOutOfScope answers a call about a project or environment the run isn't
// scoped to, as if it didn't exist.
var errOutOfScope = errors.New("not found in the project this conversation is about")

// inScope checks that projectID, and environmentID when it isn't empty, are
// the run's, when the run is scoped to a project, and notes the project for
// the audit entry.
func (c Call) inScope(ctx context.Context, projectID, environmentID string) error {
	if c.Scope.ProjectID != "" && projectID != c.Scope.ProjectID {
		return errOutOfScope
	}
	if c.Scope.EnvironmentID != "" && environmentID != "" && environmentID != c.Scope.EnvironmentID {
		return errOutOfScope
	}
	noteProject(ctx, projectID)
	return nil
}

// notFound are the errors that mean "no such thing" across the services.
var notFound = []error{
	errOutOfScope, errProjectNotFound,
	projects.ErrNotFound, projects.ErrEnvironmentNotFound, planning.ErrNotFound, deployments.ErrNotFound,
	discovery.ErrNotFound, environments.ErrNotFound, manifests.ErrNotFound, releasewatch.ErrNotFound,
}

// invalid are the errors that mean the request itself was wrong.
var invalid = []error{
	errInvalid, projects.ErrInvalid, planning.ErrInvalid, discovery.ErrInvalid, environments.ErrInvalid, manifests.ErrInvalid,
}

// errInvalid is a request an operation refuses before calling a service.
var errInvalid = errors.New("invalid request")

// ErrorKind names how a call ended for the audit log: success, forbidden,
// not_found, invalid, canceled or error.
func ErrorKind(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, security.ErrForbidden), errors.Is(err, security.ErrUnauthenticated):
		return "forbidden"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	}
	for _, e := range notFound {
		if errors.Is(err, e) {
			return "not_found"
		}
	}
	for _, e := range invalid {
		if errors.Is(err, e) {
			return "invalid"
		}
	}
	return "error"
}
