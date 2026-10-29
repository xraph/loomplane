package cloudinfra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func randomID(prefix string) (string, error) {
	v, err := typeid.Generate(prefix)
	return v.String(), err
}
func reviewToken() (string, error) {
	v := make([]byte, 32)
	if _, err := rand.Read(v); err != nil {
		return "", err
	}
	return hex.EncodeToString(v), nil
}
func capability(kind string) provider.InfrastructureCapability {
	switch kind {
	case "create":
		return provider.ServerProvisioning
	case "resize":
		return provider.ServerResize
	case "delete":
		return provider.ServerDeletion
	default:
		return provider.ServerDiscovery
	}
}

func (s *Service) PreviewServer(ctx context.Context, p security.Principal, cloud string, in ServerInput) (*Operation, error) {
	profile, err := s.authorize(ctx, p, cloud)
	if err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	if profile.Revision != in.CloudRevision {
		return nil, ErrConflict
	}
	if in.Kind != "create" && in.Kind != "adopt" && in.Kind != "resize" && in.Kind != "disconnect" && in.Kind != "delete" {
		return nil, ErrInvalid
	}
	var existing *Server
	if in.Kind == "create" || in.Kind == "adopt" {
		if in.ServerID != "" || strings.TrimSpace(in.Name) == "" || len(in.Name) > 63 || strings.ContainsAny(in.Name, "\r\n\x00") {
			return nil, ErrInvalid
		}
	} else {
		existing, err = s.Store.GetServer(ctx, p.OrgID, cloud, in.ServerID)
		if err != nil {
			return nil, err
		}
		if existing.Revision != in.ServerRevision || existing.Status == "deleted" || existing.Status == "disconnected" {
			return nil, ErrConflict
		}
		if in.Kind == "delete" && existing.Ownership != "managed" {
			return nil, fmt.Errorf("%w: adopted servers can only be disconnected", ErrInvalid)
		}
		if in.Kind == "disconnect" && existing.Ownership != "adopted" {
			return nil, fmt.Errorf("%w: managed servers require reviewed deletion", ErrInvalid)
		}
		if in.Kind == "resize" && existing.Ownership != "managed" {
			return nil, ErrInvalid
		}
		in.ConnectionID = existing.ConnectionID
	}
	var conn *connections.Connection
	var raw []byte
	if in.Kind == "disconnect" {
		conn, err = s.RuntimeConnections.GetConnection(ctx, p.OrgID, in.ConnectionID)
	} else {
		conn, raw, err = s.Connections.Credential(ctx, p.OrgID, in.ConnectionID)
	}
	if err != nil {
		return nil, err
	}
	opID, err := randomID("infraop")
	if err != nil {
		return nil, err
	}
	serverID := in.ServerID
	if serverID == "" {
		serverID, err = randomID("server")
		if err != nil {
			return nil, err
		}
	}
	review, err := reviewToken()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	o := &Operation{ID: opID, OrgID: p.OrgID, CloudID: cloud, ServerID: serverID, Review: review, Input: in, Provider: conn.Cloud, ConnectionID: conn.ID, ConnectionRevision: conn.UpdatedAt, ConnectionGeneration: conn.Generation, Status: "preview", Ownership: "managed", Principal: p, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Actions: []provider.InfrastructureAction{}, PublicIPv4: []string{}}
	if in.Kind == "adopt" || in.Kind == "disconnect" {
		if conn.Cloud != connections.Server || (!in.ConfirmAdoption && in.Kind == "adopt") {
			return nil, fmt.Errorf("%w: explicitly confirm the existing pinned SSH runtime", ErrInvalid)
		}
		o.Ownership = "adopted"
		if in.Kind == "disconnect" {
			o.AccountID = existing.AccountID
			o.ResourceID = existing.ResourceID
		} else {
			health, checkErr := s.Runtime.Inspect(ctx, raw, conn.Details, true)
			if checkErr != nil {
				return nil, checkErr
			}
			o.AccountID = health.DockerID
			o.ResourceID = health.DockerID
		}
	} else {
		credential, credErr := Credential(conn, raw)
		if credErr != nil {
			return nil, credErr
		}
		o.AccountID = credential.AccountID
		adapter, adapterErr := s.Adapters.Server(conn.Cloud, capability(in.Kind))
		if adapterErr != nil {
			return nil, adapterErr
		}
		if existing != nil {
			if existing.Provider != conn.Cloud || existing.AccountID != credential.AccountID {
				return nil, provider.ErrInfrastructureIdentity
			}
			o.ResourceID = existing.ResourceID
			if _, err = adapter.GetServer(ctx, credential, o.ResourceID); err != nil && !errors.Is(err, provider.ErrInfrastructureNotFound) {
				return nil, err
			}
			if err != nil && in.Kind != "delete" {
				return nil, err
			}
		}
		if in.Kind == "create" {
			if checkErr := s.validateEnrollmentURL(); checkErr != nil {
				return nil, checkErr
			}
			if in.Region == "" || in.Size == "" || in.Image == "" {
				return nil, ErrInvalid
			}
			if _, err = adapter.ListServers(ctx, credential); err != nil {
				return nil, err
			}
			if catalog, ok := adapter.(provider.ServerOptionsAdapter); ok {
				options, optionsErr := catalog.ServerOptions(ctx, credential)
				if optionsErr != nil {
					return nil, optionsErr
				}
				if checkErr := validateOptions(options, in); checkErr != nil {
					return nil, checkErr
				}
				for _, size := range options.Sizes {
					if size.ID != in.Size {
						continue
					}

					if priceErr := priceOperation(o, size); priceErr != nil {
						return nil, priceErr
					}
					resources, sizeErr := sizeResources(size)
					if sizeErr != nil {
						return nil, sizeErr
					}
					o.CapacityTarget = &resources
					o.CapacityCheckedAt = now
					break
				}
			}
		}
		if in.Kind == "resize" {
			if in.Size == "" {
				return nil, ErrInvalid
			}
			if catalog, ok := adapter.(provider.ServerOptionsAdapter); ok {
				options, optionsErr := catalog.ServerOptions(ctx, credential)
				if optionsErr != nil {
					return nil, optionsErr
				}
				for _, size := range options.Sizes {
					if size.ID != in.Size {
						continue
					}

					if priceErr := priceOperation(o, size); priceErr != nil {
						return nil, priceErr
					}
					resources, sizeErr := sizeResources(size)
					if sizeErr != nil {
						return nil, sizeErr
					}
					o.CapacityTarget = &resources
					o.CapacityCheckedAt = now
					break
				}
			}
		}
	}
	if checkErr := s.Store.CreatePreview(ctx, o); checkErr != nil {
		return nil, checkErr
	}
	return o, nil
}

// priceOperation puts the size's hourly price and GPUs on the review, so the
// cost is on screen before anyone approves it.
func priceOperation(o *Operation, size provider.ServerSize) error {
	o.EstimatedHourlyUSD = size.HourlyUSD
	o.HourlyPriceKnown = size.HourlyPriceKnown
	o.Accelerator = cloneAccelerator(size.Accelerator)
	var price *int64
	if size.HourlyPriceKnown {
		value, err := hourlyMicroUSD(size.HourlyUSD)
		if err != nil {
			return err
		}
		price = number(value)
	}
	identity := o.Provider + "/" + o.AccountID + "/" + size.ID
	estimate, err := moneyEstimate([]MoneyComponent{
		{Name: "server", Source: "provider_options", Identity: identity, Quantity: 1, HourlyMicroUSD: price},
		{Name: "network_egress", Source: "provider_options", Identity: identity, Quantity: 1},
		{Name: "persistent_storage", Source: "provider_options", Identity: identity, Quantity: 1},
	}, 0, o.CreatedAt)
	if err != nil {
		return err
	}
	o.MoneyEstimate = &estimate
	return nil
}

// priceServer gives a server the price and GPUs of the operation that bought or resized it.
func priceServer(v *Server, o *Operation) {
	v.Accelerator = cloneAccelerator(o.Accelerator)
	v.HourlyUSD = o.EstimatedHourlyUSD
	v.HourlyPriceKnown = o.HourlyPriceKnown
	v.PricedSize = o.Input.Size
}

func cloneAccelerator(a *provider.Accelerator) *provider.Accelerator {
	if a == nil {
		return nil
	}
	copied := *a
	return &copied
}

func connectionMatches(o *Operation, c *connections.Connection) bool {
	if o.ConnectionGeneration == 0 {
		return c.UpdatedAt.Equal(o.ConnectionRevision)
	}
	return c.Generation == o.ConnectionGeneration
}

func (s *Service) validateConnection(ctx context.Context, o *Operation) (*connections.Connection, []byte, error) {
	var conn *connections.Connection
	var raw []byte
	var err error
	if o.Input.Kind == "disconnect" {
		conn, err = s.RuntimeConnections.GetConnection(ctx, o.OrgID, o.ConnectionID)
	} else {
		conn, raw, err = s.Connections.Credential(ctx, o.OrgID, o.ConnectionID)
	}
	if err != nil {
		return nil, nil, err
	}
	if conn.OrgID != o.OrgID || conn.Cloud != o.Provider || !connectionMatches(o, conn) {
		return nil, nil, ErrConflict
	}
	if o.Ownership == "managed" {
		cred, credErr := Credential(conn, raw)
		if credErr != nil {
			return nil, nil, credErr
		}
		if cred.AccountID != o.AccountID {
			return nil, nil, provider.ErrInfrastructureIdentity
		}
	}
	return conn, raw, nil
}
func (s *Service) ApplyServerOperation(ctx context.Context, p security.Principal, cloud, id string, in ApplyInput) (*Operation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	o, err := s.Store.GetOperation(ctx, p.OrgID, cloud, id)
	if err != nil {
		return nil, err
	}
	if store, ok := s.Store.(*PgStore); ok {
		expansion, checkErr := store.expansionForOperation(ctx, o)
		if checkErr != nil {
			return nil, checkErr
		}
		if expansion != nil {
			return nil, security.ErrForbidden
		}
	}
	if o.Review != in.Review {
		return nil, ErrConflict
	}
	if o.Status != "preview" {
		return o, nil
	}
	if !s.now().Before(o.ExpiresAt) {
		return nil, ErrConflict
	}
	if _, _, err = s.validateConnection(ctx, o); err != nil {
		return nil, err
	}
	o.Status = "pending"
	o.Attempt = 1
	o.UpdatedAt = s.now().UTC()
	if checkErr := s.Store.Accept(ctx, o); checkErr != nil {
		return nil, checkErr
	}
	s.record(ctx, p, "cloud.server.accept", o)
	// The accepted row is the outbox. A failed enqueue is recovered on startup.
	if err := s.Workflow.StartInfrastructure(ctx, o.OrgID, o.ID, o.Attempt); err != nil {
		s.record(ctx, p, "cloud.server.enqueue_pending", o)
	}
	return o, nil
}
func (s *Service) RetryOperation(ctx context.Context, p security.Principal, cloud, id string) (*Operation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	var out *Operation
	err := s.Store.WithOperationLock(ctx, id, func() error {
		o, err := s.Store.GetOperation(ctx, p.OrgID, cloud, id)
		if err != nil {
			return err
		}
		out = o
		if o.Status != "failed" && o.Status != "waiting" {
			return ErrConflict
		}
		if _, _, err = s.validateConnection(ctx, o); err != nil {
			return err
		}
		o.Attempt++
		o.Status = "pending"
		o.Message = ""
		o.UpdatedAt = s.now().UTC()
		return s.Store.SaveOperation(ctx, o)
	})
	if err != nil {
		return nil, err
	}
	if err := s.Workflow.StartInfrastructure(ctx, out.OrgID, out.ID, out.Attempt); err != nil {
		s.record(ctx, p, "cloud.server.enqueue_pending", out)
	}
	return out, nil
}
func (s *Service) record(ctx context.Context, p security.Principal, action string, o *Operation) {
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", Principal: p, Attributes: map[string]any{"operation_id": o.ID, "cloud_id": o.CloudID, "server_id": o.ServerID, "kind": o.Input.Kind, "status": o.Status}})
	}
}
func (s *Service) checkpoint(ctx context.Context, o *Operation) error {
	o.UpdatedAt = s.now().UTC()
	return s.Store.SaveOperation(ctx, o)
}

// ReconcileInfrastructure is a Dispatch activity. Pending work stays durable;
// permanent failures remain visible and require an authorized retry.
func (s *Service) ReconcileInfrastructure(ctx context.Context, org, id string, heartbeat func([]byte) error) error {
	return s.Store.WithOperationLock(ctx, id, func() error {
		o, err := s.Store.OperationByID(ctx, org, id)
		if err != nil {
			return err
		}
		if o.Status == "succeeded" || o.Status == "failed" || o.Status == "preview" || o.Status == "cancelled" {
			return nil
		}
		if heartbeat != nil {
			if checkErr := heartbeat([]byte(`{"stage":"reconcile"}`)); checkErr != nil {
				return checkErr
			}
		}
		o.Status = "running"
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
		err = s.execute(ctx, o)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case errors.Is(err, ErrPending):
			o.Status = "waiting"
			o.Message = "Waiting for provider actions, enrollment or verified runtime readiness."
		case errors.Is(err, ErrDirectApplyRunning):
			// A claim refused beside a running direct apply retries once it ends.
			o.Status = "waiting"
			o.Message = "Waiting for a direct deployment on this server to finish."
			err = errors.Join(ErrPending, err)
		case err != nil:
			o.Status = "failed"
			o.Message = safeFailure(err)
		default:
			o.Status = "succeeded"
			o.Message = ""
		}
		if saveErr := s.checkpoint(ctx, o); saveErr != nil {
			return saveErr
		}
		s.record(ctx, o.Principal, "cloud.server."+o.Status, o)
		if errors.Is(err, ErrPending) {
			return ErrPending
		}
		return nil
	})
}

// CapacityBlockedPrefix starts the failure of an operation the capacity
// policy stopped in execute; the block's Summary follows it.
const CapacityBlockedPrefix = "The cloud's capacity policy blocked this server before it was created: "

func safeFailure(err error) string {
	var blocked *CapacityAdmissionError
	switch {
	// Before ErrConflict, which a capacity block also matches.
	case errors.As(err, &blocked):
		return CapacityBlockedPrefix + blocked.Block.Summary() + ". Change the policy or free capacity, then retry."
	case errors.Is(err, provider.ErrInfrastructureAmbiguous):
		return "The provider outcome is unresolved. Retry reconciles the existing intent without creating a replacement."
	case errors.Is(err, ErrConflict):
		return "The reviewed connection or server changed. Resolve the recorded operation before continuing."
	case errors.Is(err, security.ErrForbidden):
		return "Organization management access was revoked."
	case errors.Is(err, ErrDirectApplyRunning):
		return "A direct deployment is running on this server. Retry once it finishes."
	default:
		return "Infrastructure verification failed. Check the connection, provider scope and pinned server runtime before retrying."
	}
}
func (s *Service) RecoverInfrastructure(ctx context.Context) error {
	ops, err := s.Store.Pending(ctx)
	if err != nil {
		return err
	}
	for _, o := range ops {
		if checkErr := s.Workflow.StartInfrastructure(ctx, o.OrgID, o.ID, o.Attempt); checkErr != nil {
			return checkErr
		}
	}
	return s.recoverCapacity(ctx)
}

func (s *Service) execute(ctx context.Context, o *Operation) error {
	expansion, expansionErr := s.expansionEffect(ctx, o)
	if expansionErr != nil {
		return expansionErr
	}
	// Accepted cleanup keeps its recorded authority and exact resource target.
	// New capacity still requires the original manager's current permission.
	if !expansion && o.Input.Kind != "delete" && o.Input.Kind != "disconnect" {
		if checkErr := s.Authz.Require(ctx, o.Principal, security.OrganizationCloudManage, o.OrgID); checkErr != nil {
			return checkErr
		}
	}
	if !o.Attempted {
		if capacity, ok := s.Store.(interface {
			EnsureInfrastructureCapacity(context.Context, *Operation) error
		}); ok {
			if err := capacity.EnsureInfrastructureCapacity(ctx, o); err != nil {
				return err
			}
		}
		profile, scopeErr := s.Clouds.GetProfile(ctx, o.OrgID, "", o.CloudID)
		if scopeErr != nil {
			return scopeErr
		}
		if profile.Revision != o.Input.CloudRevision {
			return ErrConflict
		}
	}
	conn, raw, err := s.validateConnection(ctx, o)
	if err != nil {
		return err
	}
	v, err := s.Store.GetServer(ctx, o.OrgID, o.CloudID, o.ServerID)
	if o.Input.Kind == "adopt" && (errors.Is(err, ErrNotFound) || (err == nil && v.RuntimeConnectionID == "")) {
		// Older accepted operations did not reserve their host until execution.
		v, err = s.Store.ClaimAdoption(ctx, o.OrgID, o.CloudID, o.ID)
	}
	if errors.Is(err, ErrNotFound) && o.Input.Kind == "create" {
		v = &Server{CreationOperationID: o.ID, ID: o.ServerID, OrgID: o.OrgID, CloudID: o.CloudID, Name: o.Input.Name, Ownership: o.Ownership, Provider: o.Provider, ConnectionID: o.ConnectionID, AccountID: o.AccountID, ResourceID: o.ResourceID, Region: o.Input.Region, Size: o.Input.Size, Revision: 1, Status: "provisioning", CreatedAt: o.CreatedAt, UpdatedAt: s.now().UTC()}
		// The server bills from the moment it exists, so its price shows while it boots.
		priceServer(v, o)
		err = s.Store.SaveServer(ctx, v)
	}
	if err != nil {
		return err
	}
	if o.Input.Kind == "adopt" || o.Input.Kind == "disconnect" {
		return s.adoptOrDisconnect(ctx, o, v, conn, raw)
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return err
	}
	adapter, err := s.Adapters.Server(o.Provider, capability(o.Input.Kind))
	if err != nil {
		return err
	}
	switch o.Input.Kind {
	case "create":
		return s.createServer(ctx, o, v, adapter, credential)
	case "resize":
		return s.resizeServer(ctx, o, v, adapter, credential)
	case "delete":
		return s.deleteServer(ctx, o, v, adapter, credential)
	default:
		return ErrInvalid
	}
}

func (s *Service) createServer(ctx context.Context, o *Operation, v *Server, a provider.ServerAdapter, c provider.InfrastructureCredential) error {
	if o.ResourceID == "" {
		resumed := o.Attempted
		var userData string
		if !resumed {
			data, err := s.prepareBootstrap(ctx, o)
			if err != nil {
				return err
			}
			userData = data
			if checkErr := s.checkpointCreateAttempt(ctx, o); checkErr != nil {
				return checkErr
			}
		}
		result, err := a.CreateServer(ctx, c, provider.ServerSpec{OperationID: o.ID, Name: o.Input.Name, Region: o.Input.Region, Size: o.Input.Size, Image: o.Input.Image, UserData: userData, ReconcileOnly: resumed})
		if err != nil {
			return err
		}
		if result.Server.ID == "" || result.Server.AccountID != o.AccountID {
			return provider.ErrInfrastructureIdentity
		}
		o.ResourceID = result.Server.ID
		o.Actions = result.Actions
		o.PublicIPv4 = result.Server.PublicIPv4
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
	}
	v.ResourceID = o.ResourceID
	if checkErr := s.Store.SaveServer(ctx, v); checkErr != nil {
		return checkErr
	}
	if len(o.Actions) == 0 {
		return provider.ErrInfrastructureAmbiguous
	}
	ready, err := s.actionsComplete(ctx, o, a, c)
	if err != nil {
		return err
	}
	if !ready {
		return ErrPending
	}
	remote, err := a.GetServer(ctx, c, o.ResourceID)
	if err != nil {
		return err
	}
	o.PublicIPv4 = remote.PublicIPv4
	if checkErr := s.checkpoint(ctx, o); checkErr != nil {
		return checkErr
	}
	if !o.Enrolled {
		if !s.now().Before(o.BootstrapExpiresAt) {
			return ErrConflict
		}
		return ErrPending
	}
	return s.readyRuntime(ctx, o, v)
}
func (s *Service) actionsComplete(ctx context.Context, o *Operation, a provider.ServerAdapter, c provider.InfrastructureCredential) (bool, error) {
	for i, action := range o.Actions {
		if action.ID == "" || action.ResourceID != o.ResourceID {
			return false, provider.ErrInfrastructureIdentity
		}
		result, err := a.GetServerAction(ctx, c, o.ResourceID, action.ID)
		if err != nil {
			return false, err
		}
		if result.ID != action.ID || result.ResourceID != o.ResourceID || result.Kind != action.Kind {
			return false, provider.ErrInfrastructureIdentity
		}
		o.Actions[i] = result
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return false, checkErr
		}
		if result.Status == "errored" {
			return false, ErrConflict
		}
		if result.Status != "completed" {
			return false, nil
		}
	}
	return true, nil
}
func (s *Service) resizeServer(ctx context.Context, o *Operation, v *Server, a provider.ServerAdapter, c provider.InfrastructureCredential) error {
	if !o.Attempted {
		if useErr := s.ensureUnused(ctx, v); useErr != nil {
			return useErr
		}
		v.Status = "resizing"
		if saveErr := s.Store.SaveServer(ctx, v); saveErr != nil {
			return saveErr
		}
		o.Attempted = true
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
		action, err := a.ResizeServer(ctx, c, o.ResourceID, o.Input.Size)
		if err != nil {
			return err
		}
		o.Actions = []provider.InfrastructureAction{action}
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
	}
	if len(o.Actions) == 0 {
		return provider.ErrInfrastructureAmbiguous
	}
	done, err := s.actionsComplete(ctx, o, a, c)
	if err != nil {
		return err
	}
	if !done {
		return ErrPending
	}
	remote, err := a.GetServer(ctx, c, o.ResourceID)
	if err != nil {
		return err
	}
	if remote.Size != o.Input.Size {
		return ErrPending
	}
	v.Size = o.Input.Size
	return s.readyRuntime(ctx, o, v)
}
func (s *Service) deleteServer(ctx context.Context, o *Operation, v *Server, a provider.ServerAdapter, c provider.InfrastructureCredential) error {
	if v.Ownership != "managed" || v.ResourceID != o.ResourceID || v.AccountID != o.AccountID {
		return ErrConflict
	}
	if v.RuntimeConnectionID != "" && v.Status != "deleted" {
		if checkErr := s.ensureUnused(ctx, v); checkErr != nil {
			return checkErr
		}
	}
	_, err := a.GetServer(ctx, c, o.ResourceID)
	if errors.Is(err, provider.ErrInfrastructureNotFound) {
		o.AbsenceConfirmedAt = s.now().UTC()
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
		v.Status = "deleted"
		v.Revision++
		v.UpdatedAt = s.now().UTC()
		if checkErr := s.Store.SaveServer(ctx, v); checkErr != nil {
			return checkErr
		}
		return s.removeRuntime(ctx, o, v)
	}
	if err != nil {
		return err
	}
	if !o.Attempted {
		o.Attempted = true
		if checkErr := s.checkpoint(ctx, o); checkErr != nil {
			return checkErr
		}
	}
	// Deletion is idempotent for this recorded immutable resource ID. A lost
	// accepted response is reconciled before another DELETE of that same ID.
	if checkErr := a.DeleteServer(ctx, c, o.ResourceID); checkErr != nil {
		return checkErr
	}
	return ErrPending
}
func (s *Service) adoptOrDisconnect(ctx context.Context, o *Operation, v *Server, conn *connections.Connection, raw []byte) error {
	if o.Input.Kind == "disconnect" {
		if v.Status == "disconnected" {
			return nil
		}
		if checkErr := s.ensureUnused(ctx, v); checkErr != nil {
			return checkErr
		}
		v.Status = "disconnected"
		v.Revision++
		v.UpdatedAt = s.now().UTC()
		return s.Store.SaveServer(ctx, v)
	}
	health, err := s.Runtime.Inspect(ctx, raw, conn.Details, v.Status != "ready")
	if err != nil {
		return err
	}
	if health.DockerID != o.AccountID {
		return provider.ErrInfrastructureIdentity
	}
	v.RuntimeConnectionID = conn.ID
	v.ResourceID = health.DockerID
	v.Health = health
	v.Status = "ready"
	v.UpdatedAt = s.now().UTC()
	return s.Store.SaveServer(ctx, v)
}

// CancelOperation closes an unattempted intent or a known resource's failed
// operation. Known capacity remains in inventory for explicit reviewed cleanup.
func (s *Service) CancelOperation(ctx context.Context, p security.Principal, cloud, id string) (*Operation, error) {
	if _, err := s.authorize(ctx, p, cloud); err != nil {
		return nil, err
	}
	if p.ReadOnly {
		return nil, security.ErrForbidden
	}
	var out *Operation
	err := s.Store.WithOperationLock(ctx, id, func() error {
		o, err := s.Store.GetOperation(ctx, p.OrgID, cloud, id)
		if err != nil {
			return err
		}
		out = o
		if o.Status == "succeeded" || o.Status == "cancelled" {
			return nil
		}
		if o.Attempted && o.ResourceID == "" {
			return provider.ErrInfrastructureAmbiguous
		}

		if o.Input.Kind == "adopt" {
			v, getErr := s.Store.GetServer(ctx, o.OrgID, o.CloudID, o.ServerID)
			if getErr != nil && !errors.Is(getErr, ErrNotFound) {
				return getErr
			}
			if v != nil && v.Status != "ready" {
				v.Status = "disconnected"
				v.Revision++
				if saveErr := s.Store.SaveServer(ctx, v); saveErr != nil {
					return saveErr
				}
			}
		}
		if !o.Attempted && o.ResourceID == "" && o.Input.Kind == "create" {
			v, getErr := s.Store.GetServer(ctx, o.OrgID, o.CloudID, o.ServerID)
			if getErr != nil && !errors.Is(getErr, ErrNotFound) {
				return getErr
			}
			if v != nil {
				v.Status = "deleted"
				v.Revision++
				if saveErr := s.Store.SaveServer(ctx, v); saveErr != nil {
					return saveErr
				}
			}
			if deleteErr := s.Secrets.Delete(ctx, o.OrgID, bootstrapKey(o.ID)); deleteErr != nil {
				return deleteErr
			}
		}
		if o.ResourceID != "" && o.Input.Kind == "create" {
			v, getErr := s.Store.GetServer(ctx, o.OrgID, o.CloudID, o.ServerID)
			if getErr != nil {
				return getErr
			}
			if v.ResourceID == "" {
				v.ResourceID = o.ResourceID
				if saveErr := s.Store.SaveServer(ctx, v); saveErr != nil {
					return saveErr
				}
			}
		}
		o.Status = "cancelled"
		o.Message = "Cancelled. Recorded server capacity requires separate cleanup."
		return s.checkpoint(ctx, o)
	})
	return out, err
}
