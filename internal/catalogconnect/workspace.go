package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/security"
)

// A registered endpoint is a workspace service: a database, cache or
// bucket the workspace connects to by its connection string, described,
// labelled with the environment it serves, shared with every project or
// only some, and kept with the newest check of what its credentials may
// do. A provider resource's endpoints stay its own; none of this changes
// them.

// ErrConflict is a change to an endpoint that moved since it was read.
var ErrConflict = errors.New("the endpoint changed since it was read; read it again")

// Details are what a person says about a workspace service.
type Details struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// EnvironmentLabel is the environment it serves, such as production.
	EnvironmentLabel string `json:"environment_label"`
}

// Use is one binding that connects to an endpoint: which project, which
// environment, and the resource in its manifest.
type Use struct {
	ProjectID     string `json:"project_id"`
	Project       string `json:"project"`
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	Resource      string `json:"resource"`
}

// WorkspaceStore keeps what a workspace service adds to an endpoint row.
// *PgStore is the real one.
type WorkspaceStore interface {
	// Update writes the details, or returns ErrNameTaken or ErrNotFound.
	Update(ctx context.Context, orgID, id string, in Details, at time.Time) (*Endpoint, error)
	// Replace writes e's connection parts and moves its revision on, if
	// the row is still at revision from; else ErrConflict, or ErrNotFound.
	Replace(ctx context.Context, e *Endpoint, from int, at time.Time) error
	// SetSharing replaces who may use the endpoint.
	SetSharing(ctx context.Context, orgID, id string, restricted bool, projects []string, by string, at time.Time) error
	// SetAdmin records whether an admin credential is kept.
	SetAdmin(ctx context.Context, orgID, id string, set bool, at time.Time) error
	// SaveCheck keeps the newest check.
	SaveCheck(ctx context.Context, orgID, id string, c *LastCheck) error
	// Uses lists the bindings that connect to the endpoint.
	Uses(ctx context.Context, orgID, id string) ([]Use, error)
}

// RegisterInput registers a workspace service. Check is what the check
// that came before found, from the target in Check.Target; the caller
// runs it, since the dialer is theirs.
type RegisterInput struct {
	Details
	Engine string `json:"engine"`
	// DSN is the connection string, as CreateInput takes it.
	DSN string `json:"dsn"`
	// Restricted limits it to Projects; unrestricted, every project may
	// use it.
	Restricted bool     `json:"restricted"`
	Projects   []string `json:"projects"`
}

// Limits on what a person writes about a workspace service.
const (
	maxDescription = 200
	maxLabel       = 40
	maxProjects    = 500
)

// ProjectsIn reports which of ids are projects in orgID, so sharing never
// names one that isn't. The extension supplies it; nil accepts any.
type ProjectsIn func(ctx context.Context, orgID string, ids []string) (missing []string, err error)

func (s *Service) workspace() (WorkspaceStore, error) {
	ws, ok := s.store.(WorkspaceStore)
	if !ok {
		return nil, errors.New("this endpoint store keeps no workspace services")
	}
	return ws, nil
}

// cleanDetails trims and checks what a person wrote.
func cleanDetails(in Details) (Details, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.EnvironmentLabel = strings.TrimSpace(in.EnvironmentLabel)
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > maxName:
		return in, fmt.Errorf("%w: a workspace service needs a name of at most %d characters", ErrInvalid, maxName)
	case utf8.RuneCountInString(in.Description) > maxDescription:
		return in, fmt.Errorf("%w: keep the description to %d characters", ErrInvalid, maxDescription)
	case utf8.RuneCountInString(in.EnvironmentLabel) > maxLabel:
		return in, fmt.Errorf("%w: keep the environment label to %d characters", ErrInvalid, maxLabel)
	case strings.ContainsAny(in.Name+in.Description+in.EnvironmentLabel, "\r\n"):
		return in, fmt.Errorf("%w: a name, description or label is one line", ErrInvalid)
	}
	return in, nil
}

// cleanProjects sorts and dedupes the projects, and checks each is the
// organization's.
func (s *Service) cleanProjects(ctx context.Context, orgID string, restricted bool, ids []string) ([]string, error) {
	out := []string{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	if len(out) > maxProjects {
		return nil, fmt.Errorf("%w: share with at most %d projects, or with every project", ErrInvalid, maxProjects)
	}
	if !restricted {
		// Shared with every project, a list would mean nothing.
		return []string{}, nil
	}
	if s.ProjectsIn != nil && len(out) > 0 {
		missing, err := s.ProjectsIn(ctx, orgID, out)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("%w: no project %s in this workspace", ErrInvalid, strings.Join(missing, ", "))
		}
	}
	return out, nil
}

// Register stores a workspace service its check passed: its DSN in
// Vault, its details and sharing on the row, and the check beside them.
// It needs SecretManage. A check that failed is refused, so nothing
// unusable is stored.
func (s *Service) Register(ctx context.Context, p security.Principal, in RegisterInput, check *LastCheck) (e *Endpoint, err error) {
	defer func() {
		event := AuditEvent{Action: AuditCreate, Principal: p, Engine: in.Engine, Address: dsnAddress(in.DSN), Outcome: outcomeOf(err)}
		if err == nil {
			event.EndpointID = e.ID
		}
		s.record(ctx, event)
	}()
	if cerr := s.authz.Require(ctx, p, security.SecretManage, ""); cerr != nil {
		return nil, cerr
	}
	ws, err := s.workspace()
	if err != nil {
		return nil, err
	}
	details, err := cleanDetails(in.Details)
	if err != nil {
		return nil, err
	}
	if check == nil || !check.OK {
		return nil, fmt.Errorf("%w: check the connection before registering it, and fix what the check found", ErrInvalid)
	}
	projects, err := s.cleanProjects(ctx, p.OrgID, in.Restricted, in.Projects)
	if err != nil {
		return nil, err
	}
	created, err := s.create(ctx, p, CreateInput{Name: details.Name, Engine: in.Engine, DSN: in.DSN},
		func(e *Endpoint) {
			e.Description, e.EnvironmentLabel, e.Restricted = details.Description, details.EnvironmentLabel, in.Restricted
		})
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if in.Restricted {
		if cerr := ws.SetSharing(ctx, p.OrgID, created.ID, true, projects, p.UserID, now); cerr != nil {
			return nil, s.undoRegister(ctx, p, created.ID, cerr)
		}
	}
	saved := *check
	saved.At = now
	if cerr := ws.SaveCheck(ctx, p.OrgID, created.ID, &saved); cerr != nil {
		return nil, s.undoRegister(ctx, p, created.ID, cerr)
	}
	return s.store.Get(ctx, p.OrgID, created.ID)
}

// undoRegister removes a half-registered endpoint, so a retry can use the
// name again.
func (s *Service) undoRegister(ctx context.Context, p security.Principal, id string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if err := s.store.Delete(ctx, p.OrgID, id); err != nil {
		return errors.Join(cause, err)
	}
	if err := s.secrets.Delete(ctx, DSNKey(p.OrgID, id)); err != nil && s.OrphanedDSN != nil {
		s.OrphanedDSN(ctx, DSNKey(p.OrgID, id), err)
	}
	return cause
}

// registered reads an endpoint p may change: one a person registered.
// It needs SecretManage.
func (s *Service) registered(ctx context.Context, p security.Principal, id string) (*Endpoint, WorkspaceStore, error) {
	if err := s.authz.Require(ctx, p, security.SecretManage, ""); err != nil {
		return nil, nil, err
	}
	ws, err := s.workspace()
	if err != nil {
		return nil, nil, err
	}
	e, err := s.store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, nil, err
	}
	if !e.Registered() {
		return nil, nil, fmt.Errorf("%w: %s belongs to a provider resource; change it there", ErrInvalid, e.Name)
	}
	return e, ws, nil
}

// Describe returns one endpoint. It needs InfrastructureRead.
func (s *Service) Describe(ctx context.Context, p security.Principal, id string) (*Endpoint, error) {
	if err := s.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, p.OrgID, id)
}

// Update changes a workspace service's name, description and label. It
// needs SecretManage.
func (s *Service) Update(ctx context.Context, p security.Principal, id string, in Details) (out *Endpoint, err error) {
	event := AuditEvent{Action: AuditUpdate, Principal: p, EndpointID: id}
	defer func() {
		event.Outcome = outcomeOf(err)
		s.record(ctx, event)
	}()
	e, ws, err := s.registered(ctx, p, id)
	if err != nil {
		return nil, err
	}
	event.Engine, event.Address = e.Engine, e.address()
	details, err := cleanDetails(in)
	if err != nil {
		return nil, err
	}
	return ws.Update(ctx, p.OrgID, id, details, s.now().UTC())
}

// Replace swaps a workspace service's credentials for ones the check in
// check passed, if it's still at revision. The engine stays; the DSN may
// point somewhere else, such as a new primary. Services get the new
// credentials the next time they deploy. It needs SecretManage.
func (s *Service) Replace(ctx context.Context, p security.Principal, id string, revision int, dsn string, check *LastCheck) (out *Endpoint, err error) {
	event := AuditEvent{Action: AuditReplace, Principal: p, EndpointID: id, Address: dsnAddress(dsn)}
	defer func() {
		event.Outcome = outcomeOf(err)
		s.record(ctx, event)
	}()
	e, ws, err := s.registered(ctx, p, id)
	if err != nil {
		return nil, err
	}
	event.Engine = e.Engine
	if e.Revision != revision {
		return nil, ErrConflict
	}
	if check == nil || !check.OK {
		return nil, fmt.Errorf("%w: check the new connection before replacing the old one, and fix what the check found", ErrInvalid)
	}
	next := *e
	if cerr := parseDSN(&next, dsn); cerr != nil {
		return nil, cerr
	}
	key := DSNKey(e.OrgID, e.ID)
	previous, err := s.secrets.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read the current credentials of %s: %w", e.Name, err)
	}
	if cerr := s.secrets.Put(ctx, key, []byte(strings.TrimSpace(dsn))); cerr != nil {
		return nil, cerr
	}
	now := s.now().UTC()
	if cerr := ws.Replace(ctx, &next, revision, now); cerr != nil {
		// The row still names the old endpoint, so its credentials go back.
		if rerr := s.secrets.Put(context.WithoutCancel(ctx), key, previous); rerr != nil {
			return nil, errors.Join(cerr, fmt.Errorf("put the previous credentials back: %w", rerr))
		}
		return nil, cerr
	}
	saved := *check
	saved.At = now
	if e.LastCheck != nil {
		saved.Admin = e.LastCheck.Admin
	}
	if cerr := ws.SaveCheck(ctx, p.OrgID, id, &saved); cerr != nil {
		return nil, cerr
	}
	return s.store.Get(ctx, p.OrgID, id)
}

// SetAdmin keeps an admin credential beside a workspace service's own,
// for creating a database or a user for each service that connects. It
// must be the same engine on the same host and port, and its check must
// have passed. It needs SecretManage.
func (s *Service) SetAdmin(ctx context.Context, p security.Principal, id, dsn string, check *LastCheck) (out *Endpoint, err error) {
	event := AuditEvent{Action: AuditAdmin, Principal: p, EndpointID: id, Address: dsnAddress(dsn)}
	defer func() {
		event.Outcome = outcomeOf(err)
		s.record(ctx, event)
	}()
	e, ws, err := s.registered(ctx, p, id)
	if err != nil {
		return nil, err
	}
	event.Engine = e.Engine
	if check == nil || !check.OK {
		return nil, fmt.Errorf("%w: check the admin connection before keeping it, and fix what the check found", ErrInvalid)
	}
	parsed := &Endpoint{Engine: e.Engine}
	if cerr := parseDSN(parsed, dsn); cerr != nil {
		return nil, cerr
	}
	if parsed.Host != e.Host || parsed.Port != e.Port {
		return nil, fmt.Errorf("%w: the admin connection must reach %s, as %s does", ErrInvalid, e.address(), e.Name)
	}
	if cerr := s.secrets.Put(ctx, AdminDSNKey(e.OrgID, e.ID), []byte(strings.TrimSpace(dsn))); cerr != nil {
		return nil, cerr
	}
	now := s.now().UTC()
	if cerr := ws.SetAdmin(ctx, p.OrgID, id, true, now); cerr != nil {
		return nil, cerr
	}
	saved := LastCheck{At: now}
	if e.LastCheck != nil {
		saved = *e.LastCheck
	}
	saved.Admin = check.Capabilities
	if saved.Admin == nil {
		saved.Admin = &Capabilities{}
	}
	if cerr := ws.SaveCheck(ctx, p.OrgID, id, &saved); cerr != nil {
		return nil, cerr
	}
	return s.store.Get(ctx, p.OrgID, id)
}

// ClearAdmin forgets a workspace service's admin credential. It needs
// SecretManage.
func (s *Service) ClearAdmin(ctx context.Context, p security.Principal, id string) (out *Endpoint, err error) {
	event := AuditEvent{Action: AuditAdmin, Principal: p, EndpointID: id}
	defer func() {
		event.Outcome = outcomeOf(err)
		s.record(ctx, event)
	}()
	e, ws, err := s.registered(ctx, p, id)
	if err != nil {
		return nil, err
	}
	event.Engine, event.Address = e.Engine, e.address()
	if cerr := ws.SetAdmin(ctx, p.OrgID, id, false, s.now().UTC()); cerr != nil {
		return nil, cerr
	}
	if cerr := s.secrets.Delete(ctx, AdminDSNKey(e.OrgID, e.ID)); cerr != nil {
		if s.OrphanedDSN != nil {
			s.OrphanedDSN(ctx, AdminDSNKey(e.OrgID, e.ID), cerr)
		}
		return nil, fmt.Errorf("the admin credential is forgotten, but it's still in Vault: %w", cerr)
	}
	if e.LastCheck != nil && e.LastCheck.Admin != nil {
		saved := *e.LastCheck
		saved.Admin = nil
		if cerr := ws.SaveCheck(ctx, p.OrgID, id, &saved); cerr != nil {
			return nil, cerr
		}
	}
	return s.store.Get(ctx, p.OrgID, id)
}

// Share sets who may use a workspace service: every project, or only the
// ones listed. Revoking a project stops its next plan from using it;
// what it already deployed keeps running until then. It needs
// SecretManage.
func (s *Service) Share(ctx context.Context, p security.Principal, id string, restricted bool, projects []string) (out *Endpoint, err error) {
	event := AuditEvent{Action: AuditShare, Principal: p, EndpointID: id}
	defer func() {
		event.Outcome = outcomeOf(err)
		s.record(ctx, event)
	}()
	e, ws, err := s.registered(ctx, p, id)
	if err != nil {
		return nil, err
	}
	event.Engine, event.Address = e.Engine, e.address()
	list, err := s.cleanProjects(ctx, p.OrgID, restricted, projects)
	if err != nil {
		return nil, err
	}
	if cerr := ws.SetSharing(ctx, p.OrgID, id, restricted, list, p.UserID, s.now().UTC()); cerr != nil {
		return nil, cerr
	}
	return s.store.Get(ctx, p.OrgID, id)
}

// SaveCheck keeps a check of a stored workspace service that p ran. It
// needs SecretManage, as running one does.
func (s *Service) SaveCheck(ctx context.Context, p security.Principal, id string, check LastCheck) (*Endpoint, error) {
	e, ws, err := s.registered(ctx, p, id)
	if err != nil {
		return nil, err
	}
	check.At = s.now().UTC()
	if e.LastCheck != nil && check.Admin == nil {
		check.Admin = e.LastCheck.Admin
	}
	if cerr := ws.SaveCheck(ctx, p.OrgID, id, &check); cerr != nil {
		return nil, cerr
	}
	return s.store.Get(ctx, p.OrgID, id)
}

// Uses lists what connects to an endpoint. It needs InfrastructureRead.
func (s *Service) Uses(ctx context.Context, p security.Principal, id string) ([]Use, error) {
	if err := s.authz.Require(ctx, p, security.InfrastructureRead, ""); err != nil {
		return nil, err
	}
	ws, err := s.workspace()
	if err != nil {
		return nil, err
	}
	if _, err = s.store.Get(ctx, p.OrgID, id); err != nil {
		return nil, err
	}
	return ws.Uses(ctx, p.OrgID, id)
}

// ResolveAdmin returns a workspace service's admin credential for
// Loomplane's own use, such as creating a service's database. It checks
// no principal: callers have already authorized the work.
func (s *Service) ResolveAdmin(ctx context.Context, orgID, id string) (*Endpoint, string, error) {
	e, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return nil, "", err
	}
	if !e.AdminSet {
		return nil, "", fmt.Errorf("%w: %s keeps no admin credential", ErrInvalid, e.Name)
	}
	dsn, err := s.secrets.Get(ctx, AdminDSNKey(orgID, id))
	if err != nil {
		return nil, "", fmt.Errorf("read the admin credential of %s: %w", e.Name, err)
	}
	return e, string(dsn), nil
}
