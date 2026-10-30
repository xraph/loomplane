package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

type registryCredentials interface {
	RefreshRegistry(context.Context, string, string) (*connections.Connection, []byte, error)
	RegistryCredential(context.Context, string, string) (*connections.Connection, []byte, error)
	WithRegistryLock(context.Context, string, string, func() error) error
}

func (s *Service) GetRegistry(ctx context.Context, p security.Principal, id string) (*RegistryState, error) {
	if operationErr := s.domainScope(ctx, p, "", false); operationErr != nil {
		return nil, operationErr
	}
	st, err := s.registryStore()
	if err != nil {
		return nil, err
	}
	state, err := st.RegistryState(ctx, p.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		c, getErr := s.RuntimeConnections.GetConnection(ctx, p.OrgID, id)
		if getErr != nil {
			return nil, getErr
		}
		if c.Cloud != connections.Kubernetes || c.Details["registry_hostname"] == "" {
			return nil, ErrNotFound
		}
		return newRegistryState(c, s.now()), nil
	}
	return state, err
}
func newRegistryState(c *connections.Connection, now time.Time) *RegistryState {
	return &RegistryState{Authority: security.Principal{OrgID: c.OrgID, UserID: c.CreatedBy}, OrgID: c.OrgID, ConnectionID: c.ID, Generation: c.Generation, Identity: connections.AccountIdentity(c.Cloud, c.Account, c.Details), Hostname: c.Details["registry_hostname"], Status: "waiting", Backups: []RegistryBackup{}, UpdatedAt: now}
}
func (s *Service) RequestRegistryAction(ctx context.Context, p security.Principal, id string, in RegistryActionInput) (*RegistryState, error) {
	if operationErr := s.domainScope(ctx, p, "", true); operationErr != nil {
		return nil, operationErr
	}
	switch in.Kind {
	case "sync", "backup", "restore", "rotate", "cleanup", "upgrade", "remove", "retry":
	default:
		return nil, ErrInvalid
	}
	st, err := s.registryStore()
	if err != nil {
		return nil, err
	}
	var result *RegistryState
	err = st.WithOperationLock(ctx, "registry/"+p.OrgID+"/"+id, func() error {
		c, e := s.RuntimeConnections.GetConnection(ctx, p.OrgID, id)
		if e != nil {
			return e
		}
		if c.Cloud != connections.Kubernetes || c.Details["registry_hostname"] == "" || c.Generation != in.Generation || c.Details["registry_hostname"] != in.Hostname {
			return ErrConflict
		}
		state, e := st.RegistryState(ctx, p.OrgID, id)
		if errors.Is(e, ErrNotFound) {
			state = newRegistryState(c, s.now())
		} else if e != nil {
			return e
		}
		if state.Status == "removed" || (state.Action != nil && in.Kind != "retry") {
			return ErrBusy
		}
		if state.Hostname != in.Hostname || state.Identity != connections.AccountIdentity(c.Cloud, c.Account, c.Details) {
			return ErrConflict
		}
		if in.Kind == "retry" {
			if state.Action == nil {
				return ErrInvalid
			}
			// Explicit review renews authority without losing operation checkpoints.
			state.Generation = c.Generation
			state.Authority = p
			state.Status = "queued"
			state.Message = ""
			state.UpdatedAt = s.now().UTC()
			if saveErr := st.SaveRegistryState(ctx, state); saveErr != nil {
				return saveErr
			}
			result = state
			return nil
		}
		if in.Kind == "restore" {
			if registryBackup(state, in.BackupID) == nil {
				return ErrNotFound
			}
		}
		if in.RemoveSetup && !in.DeleteData {
			return fmt.Errorf("%w: deleting shared setup requires deleting image storage after backup", ErrInvalid)
		}
		if (in.DeleteData || in.RemoveSetup) && in.Kind != "remove" {
			return ErrInvalid
		}
		if in.Kind == "remove" {
			used, useErr := st.RegistryInUse(ctx, p.OrgID, id)
			if useErr != nil {
				return useErr
			}
			if used {
				return fmt.Errorf("%w: detach this connection's workloads and targets before removing its registry", ErrBusy)
			}
		}
		if (in.Kind == "backup" || in.Kind == "restore" || in.Kind == "cleanup" || (in.Kind == "remove" && in.DeleteData)) && s.RegistryFiles == nil {
			return fmt.Errorf("%w: backup storage is not configured", ErrInvalid)
		}
		key, keyErr := typeid.Generate("regop")
		if keyErr != nil {
			return keyErr
		}
		state.Generation = c.Generation
		state.Authority = p
		state.Action = &RegistryAction{ID: key.String(), Kind: in.Kind, Phase: "pending", StartedAt: s.now().UTC(), BackupID: in.BackupID, DeleteData: in.DeleteData, RemoveSetup: in.RemoveSetup}
		state.Status = "queued"
		state.Message = ""
		state.UpdatedAt = s.now().UTC()
		if saveErr := st.SaveRegistryState(ctx, state); saveErr != nil {
			return saveErr
		}
		result = state
		return nil
	})
	if err == nil {
		s.domainAudit(ctx, p, "registry."+in.Kind, id, "")
	}
	return result, err
}
func registryBackup(state *RegistryState, id string) *RegistryBackup {
	for i := range state.Backups {
		if state.Backups[i].ID == id {
			return &state.Backups[i]
		}
	}
	return nil
}

func (s *Service) RecoverManagedRegistries(ctx context.Context) error {
	st, err := s.registryStore()
	if err != nil {
		return err
	}
	candidates, err := st.RegistryCandidates(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = st.WithOperationLock(ctx, "registry/"+candidate.OrgID+"/"+candidate.ID, func() error {
			credentials, ok := s.Connections.(registryCredentials)
			if !ok {
				return ErrInvalid
			}
			current, currentErr := s.RuntimeConnections.GetConnection(ctx, candidate.OrgID, candidate.ID)
			if currentErr != nil {
				return currentErr
			}
			candidate = current
			saved, stateErr := st.RegistryState(ctx, candidate.OrgID, candidate.ID)
			if errors.Is(stateErr, ErrNotFound) {
				saved = newRegistryState(candidate, s.now())
			} else if stateErr != nil {
				return stateErr
			}
			if saved.Status == "removed" {
				return nil
			}
			p := saved.Authority
			denied := func(cause error) error {
				saved.Status = "needs_attention"
				saved.Message = "Registry automation needs an organization cloud manager to renew access. Queue an action or review and retry the current operation."
				saved.UpdatedAt = s.now().UTC()
				return errors.Join(cause, st.SaveRegistryState(ctx, saved))
			}
			if p.OrgID != candidate.OrgID || s.CapacityPrincipal == nil {
				return denied(security.ErrForbidden)
			}
			if authErr := s.CapacityPrincipal.ValidatePrincipal(ctx, p); authErr != nil {
				return denied(authErr)
			}
			if authErr := s.domainScope(ctx, p, "", true); authErr != nil {
				return denied(authErr)
			}

			// DNS must progress while a first build holds the registry lock
			// waiting for its TLS certificate. It does not access registry data.
			var dnsErr error
			removing := saved.Action != nil && saved.Action.Kind == "remove"
			if !removing && candidate.Details["ingress_address"] != "" && candidate.Details["registry_dns"] != "manual" {
				if saved.Identity == "" || saved.Identity != connections.AccountIdentity(candidate.Cloud, candidate.Account, candidate.Details) || saved.Hostname != candidate.Details["registry_hostname"] {
					return ErrConflict
				}
				dnsErr = s.reconcileRegistryDNS(ctx, st, saved, candidate, false)
			}
			return credentials.WithRegistryLock(ctx, candidate.OrgID, candidate.ID, func() error {
				runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
				defer cancel()
				return s.reconcileManagedRegistry(runCtx, st, credentials, candidate, dnsErr)
			})
		})
		if err != nil && !errors.Is(err, ErrBusy) {
			failures = append(failures, fmt.Errorf("registry %s: %w", candidate.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) reconcileManagedRegistry(ctx context.Context, st RegistryStore, credentials registryCredentials, candidate *connections.Connection, dnsErr error) (result error) {
	state, err := st.RegistryState(ctx, candidate.OrgID, candidate.ID)
	if errors.Is(err, ErrNotFound) {
		state = newRegistryState(candidate, s.now())
	} else if err != nil {
		return err
	}
	if state.Status == "removed" {
		return nil
	}
	defer func() {
		state.UpdatedAt = s.now().UTC()
		if result != nil {
			state.Status = "needs_attention"
			state.Message = result.Error()
			if errors.Is(result, ErrPending) || errors.Is(result, kubernetes.ErrRegistryPending) {
				state.Status = "waiting"
			}
			result = errors.Join(result, st.SaveRegistryState(context.WithoutCancel(ctx), state))
		}
	}()
	c, raw, err := credentials.RefreshRegistry(ctx, candidate.OrgID, candidate.ID)
	if err != nil {
		return err
	}
	if state.Identity == "" || state.Identity != connections.AccountIdentity(c.Cloud, c.Account, c.Details) || state.Hostname != c.Details["registry_hostname"] {
		return ErrConflict
	}
	if state.Action != nil && state.Generation != c.Generation {
		return ErrConflict
	}
	state.Generation = c.Generation
	config, err := kubernetes.ParseConfig(raw, c.Details)
	if err != nil {
		return err
	}
	client, err := kubernetes.New(config)
	if err != nil {
		return err
	}

	if state.Action == nil {
		if c.Details["managed_builds"] != "ready" {
			return fmt.Errorf("%w: cluster prerequisites are still starting", ErrPending)
		}
		exists, existsErr := client.ManagedRegistryExists(ctx, c.OrgID, c.ID)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			if dnsErr != nil {
				return dnsErr
			}
			state.Status = "ready"
			state.Message = "Your first deployment creates registry image storage."
			return st.SaveRegistryState(ctx, state)
		}
		if c.Details["registry_maintenance"] == "manual" {
			if dnsErr != nil {
				return dnsErr
			}
			state.Status = "ready"
			state.Message = "Automatic maintenance is disabled."
			return st.SaveRegistryState(ctx, state)
		}
		kind := ""
		switch {
		case state.LastUpgrade.IsZero() || s.now().Sub(state.LastUpgrade) > 24*time.Hour:
			kind = "upgrade"
		case state.LastRotation.IsZero() || s.now().Sub(state.LastRotation) > 30*24*time.Hour:
			kind = "rotate"
		case s.RegistryFiles == nil:
			return fmt.Errorf("%w: configure Loomplane backup storage to enable registry backups", ErrPending)
		case len(state.Backups) == 0 || s.now().Sub(state.Backups[len(state.Backups)-1].CreatedAt) > 24*time.Hour:
			kind = "backup"
		case state.LastCleanup.IsZero() || s.now().Sub(state.LastCleanup) > 7*24*time.Hour:
			kind = "cleanup"
		default:
			if dnsErr != nil {
				return dnsErr
			}
			state.Status = "ready"
			state.Message = ""
			return st.SaveRegistryState(ctx, state)
		}
		key, keyErr := typeid.Generate("regop")
		if keyErr != nil {
			return keyErr
		}
		state.Action = &RegistryAction{ID: key.String(), Kind: kind, Phase: "pending", StartedAt: s.now().UTC()}
		if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
			return operationErr
		}
	}
	action := state.Action
	state.Status = "running"
	state.Message = ""
	switch action.Kind {
	case "sync":
		if dnsErr != nil {
			return dnsErr
		}
	case "upgrade":
		if operationErr := client.UpgradeManagedBootstrap(ctx); operationErr != nil {
			return operationErr
		}
		state.LastUpgrade = s.now().UTC()
	case "rotate":
		if operationErr := client.RotateManagedRegistry(ctx, c.OrgID, c.ID, action.ID, s.now()); operationErr != nil {
			return operationErr
		}
		state.LastRotation = s.now().UTC()
	case "backup", "cleanup", "restore":
		if operationErr := s.registryFilesystemAction(ctx, st, state, client); operationErr != nil {
			return operationErr
		}
	case "remove":
		used, useErr := st.RegistryInUse(ctx, c.OrgID, c.ID)
		if useErr != nil {
			return useErr
		}
		if used {
			return ErrBusy
		}
		if action.DeleteData && action.Phase != "remove" {
			if operationErr := s.registryFilesystemAction(ctx, st, state, client); operationErr != nil {
				return operationErr
			}
			action.Phase = "remove"
			if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
				return operationErr
			}
		}
		if action.RemoveSetup {
			shared, sharedErr := st.RegistrySetupInUse(ctx, c)
			if sharedErr != nil {
				return sharedErr
			}
			if shared {
				return fmt.Errorf("%w: another connection still uses this cluster", ErrBusy)
			}
		}
		if operationErr := client.RemoveManagedRegistry(ctx, c.OrgID, c.ID, action.DeleteData); operationErr != nil {
			return operationErr
		}
		if operationErr := s.reconcileRegistryDNS(ctx, st, state, c, true); operationErr != nil {
			return operationErr
		}
		if action.RemoveSetup {
			if operationErr := client.RemoveManagedBootstrap(ctx); operationErr != nil {
				return operationErr
			}
		}
		state.Status = "removed"
	default:
		return ErrInvalid
	}
	state.Action = nil
	if state.Status != "removed" {
		state.Status = "ready"
	}
	state.Message = ""
	state.UpdatedAt = s.now().UTC()
	if dnsErr != nil {
		return dnsErr
	}
	return st.SaveRegistryState(ctx, state)
}

func (s *Service) registryFilesystemAction(ctx context.Context, st RegistryStore, state *RegistryState, client *kubernetes.Client) error {
	action := state.Action
	if action.Phase == "resume" {
		return client.EndRegistryMaintenance(ctx, state.OrgID, state.ConnectionID, action.ID)
	}
	session, err := client.BeginRegistryMaintenance(ctx, state.OrgID, state.ConnectionID, action.ID)
	if err != nil {
		return err
	}
	if registryBackup(state, action.ID) == nil {
		attempt, keyErr := typeid.Generate("regbackup")
		if keyErr != nil {
			return keyErr
		}
		object, backupErr := session.Backup(ctx, s.RegistryFiles, "registries/"+state.OrgID+"/"+state.ConnectionID+"/"+attempt.String()+".tar")
		if backupErr != nil {
			return backupErr
		}
		state.Backups = append(state.Backups, RegistryBackup{ID: action.ID, CreatedAt: s.now().UTC(), Object: object, Hostname: state.Hostname, NamespaceUID: client.Config.Details["namespace_uid"]})
		action.Phase = "backed_up"
		if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
			return operationErr
		}
	}
	switch action.Kind {
	case "cleanup":
		if operationErr := session.CollectGarbage(ctx); operationErr != nil {
			return operationErr
		}
		state.LastCleanup = s.now().UTC()
	case "restore":
		backup := registryBackup(state, action.BackupID)
		if backup == nil || backup.Hostname != state.Hostname || backup.NamespaceUID != client.Config.Details["namespace_uid"] {
			return ErrConflict
		}
		if operationErr := session.Restore(ctx, s.RegistryFiles, backup.Object); operationErr != nil {
			return operationErr
		}
	}
	action.Phase = "resume"
	if operationErr := st.SaveRegistryState(ctx, state); operationErr != nil {
		return operationErr
	}
	return client.EndRegistryMaintenance(ctx, state.OrgID, state.ConnectionID, action.ID)
}
