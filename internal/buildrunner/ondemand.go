package buildrunner

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

// Revoker revokes a runner whose machine is gone, so its key can't connect
// again. The nodes service satisfies it.
type Revoker interface {
	RevokeRunner(ctx context.Context, orgID, nodeID string) error
}

// MachineLabel is the label a machine's join token puts on its runner, so
// the runner that joins is linked to the machine started for it.
const MachineLabel = "runner_machine"

// On-demand defaults, when a pool sets none.
const (
	// bootDeadline is how long a machine may take to start and join.
	bootDeadline = 10 * time.Minute
	// offlineGrace is how long a ready machine's runner may be offline
	// before the machine is replaced.
	offlineGrace     = 5 * time.Minute
	defaultIdle      = 10 * time.Minute
	defaultMaxBuilds = 50
	defaultMaxAge    = 24 * time.Hour
	// DefaultRunnerImage is the base image on-demand runners boot.
	DefaultRunnerImage = "ubuntu-24-04-x64"
	// joinLifetime is how long an on-demand runner's join token lives.
	joinLifetime = 15 * time.Minute
)

var (
	sha256Hex   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	machineName = regexp.MustCompile(`^runner-[a-z0-9]{1,40}$`)
	amiPattern  = regexp.MustCompile(`^ami-[0-9a-f]{8,17}$`)
)

// validToolchain checks an on-demand pool's toolchain bundle: an HTTPS URL
// and its sha256, which the runner checks before it unpacks anything.
func validToolchain(s PoolSettings) error {
	u, err := url.Parse(s.ToolchainURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(s.ToolchainURL, " '\"\\\n\r$`") {
		return fmt.Errorf("%w: an on-demand pool names its Firecracker toolchain bundle as an https URL", ErrInvalid)
	}
	if !sha256Hex.MatchString(s.ToolchainSHA256) {
		return fmt.Errorf("%w: an on-demand pool pins its toolchain bundle's sha256", ErrInvalid)
	}
	return nil
}

// poolTag is the cloud tag every machine of a pool carries.
func poolTag(poolID string) string { return "loomplane-pool-" + poolID }

// machines is the service's machine store: its store, when that keeps
// machines too.
func (s *Service) machines() (MachineStore, error) {
	store, err := s.st()
	if err != nil {
		return nil, err
	}
	ms, ok := store.(MachineStore)
	if !ok {
		return nil, errors.New("on-demand pools aren't available here")
	}
	return ms, nil
}

// takesOnDemand reports whether m's runner may take a build now: it joined,
// and it is warm or a cold machine that hasn't had its one build.
func takesOnDemand(m *Machine) bool {
	return m.State == MachineReady && (m.Warm || (m.Builds == 0 && m.InFlight == 0))
}

// demand starts a cold machine for pool when a build found no runner free,
// unless one is starting already, which takes the build, or the pool runs
// as many as it may. The build waits for a runner either way.
func (s *Service) demand(ctx context.Context, pool *Pool) error {
	ms, err := s.machines()
	if err != nil {
		return err
	}
	machines, err := ms.ListMachines(ctx, pool.ID)
	if err != nil {
		return err
	}
	live := 0
	for _, m := range machines {
		if m.State == MachineRetiring {
			continue
		}
		live++
		if (m.State == MachineCreating || m.State == MachineBooting) && !m.Warm {
			return nil
		}
	}
	if live >= pool.Settings.MaxRunners {
		return nil
	}
	return s.startMachine(ctx, ms, pool, false)
}

// startMachine records a machine, mints its single-use join token as the
// pool's creator, and asks the pool's cloud for it.
func (s *Service) startMachine(ctx context.Context, ms MachineStore, pool *Pool, warm bool) error {
	if s.Provisioner == nil || s.Tokens == nil {
		return errors.New("on-demand pools aren't available here")
	}
	id, err := typeid.Generate("rmach")
	if err != nil {
		return err
	}
	now := s.now()
	m := &Machine{ID: id.String(), PoolID: pool.ID, OrgID: pool.OrgID, Name: "runner-" + strings.TrimPrefix(id.String(), "rmach_"),
		Region: pool.Settings.Region, Size: pool.Settings.Sizes[0], Warm: warm, State: MachineCreating, CreatedAt: now, ActiveAt: now}
	if xerr := ms.CreateMachine(ctx, m); xerr != nil {
		return xerr
	}
	// The token acts as the pool's creator, whose live role the mint and
	// the join both check, so a creator who left can't start runners.
	token, _, err := s.Tokens.MintRunnerJoin(ctx, security.Principal{OrgID: pool.OrgID, UserID: pool.CreatedBy}, pool.ID, m.ID, joinLifetime)
	if err != nil {
		m.State, m.Reason = MachineRetiring, "its join token couldn't be made as the pool's creator: "+err.Error()
		return errors.Join(err, ms.UpdateMachine(ctx, m))
	}
	image := pool.Settings.Image
	if image == "" {
		image = DefaultRunnerImage
	}
	spec := MachineSpec{ID: m.ID, Name: m.Name, Region: m.Region, Size: m.Size, Image: image, PoolTag: poolTag(pool.ID), Spot: pool.Settings.Spot,
		Tags: []string{"loomplane-runner", poolTag(pool.ID)}, UserData: UserData(s.PublicURL, token, m.Name, pool.Settings)}
	providerID, err := s.Provisioner.Create(ctx, pool.OrgID, pool.Settings.CloudConnectionID, spec)
	if err != nil {
		// The create may have run. The next reconcile looks for it by
		// its operation, so this stays creating.
		m.Reason = "the cloud didn't confirm the machine: " + err.Error()
		return errors.Join(err, ms.UpdateMachine(ctx, m))
	}
	m.ProviderID, m.State, m.Reason = providerID, MachineBooting, ""
	return ms.UpdateMachine(ctx, m)
}

// UserData boots an on-demand runner: it locks the machine down, installs
// the signed loomctl this Loomplane runs through node.sh, and starts a
// runner that installs the pinned Firecracker toolchain, joins with token
// and leaves Loomplane when its machine goes. The runner opens no port.
func UserData(publicURL, token, name string, s PoolSettings) string {
	return strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"# A Loomplane runner: it builds in Firecracker microVMs and opens no port.",
		"passwd -l root || true",
		"systemctl disable --now ssh.socket ssh.service sshd.service 2>/dev/null || true",
		"curl -fsSL '" + strings.TrimRight(publicURL, "/") + "/install/node.sh' | LOOMPLANE_ROLE=runner LOOMPLANE_JOIN_TOKEN='" + token + "' sh -s -- --name '" + name +
			"' --ephemeral --toolchain '" + s.ToolchainURL + "' --toolchain-sha256 '" + s.ToolchainSHA256 + "'",
		"",
	}, "\n")
}

// Reconcile keeps every on-demand pool to its settings: it finds machines
// whose create answered late, removes those that never joined, took their
// one build, went offline, were built on long enough or sat idle, keeps the
// warm ones the pool wants, and deletes what the cloud runs for a pool that
// Loomplane doesn't know. The cron leader runs it every minute.
func (s *Service) Reconcile(ctx context.Context) error {
	ms, err := s.machines()
	if err != nil {
		return err
	}
	pools, err := ms.OnDemandPools(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for i := range pools {
		if perr := s.reconcilePool(ctx, ms, &pools[i]); perr != nil {
			errs = append(errs, fmt.Errorf("pool %s: %w", pools[i].ID, perr))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) reconcilePool(ctx context.Context, ms MachineStore, pool *Pool) error {
	if s.Provisioner == nil {
		return errors.New("on-demand pools aren't available here")
	}
	machines, err := ms.ListMachines(ctx, pool.ID)
	if err != nil {
		return err
	}
	now := s.now()
	idle := minutesOr(pool.Settings.IdleTimeoutMinutes, defaultIdle)
	maxAge := minutesOr(pool.Settings.MaxAgeMinutes, defaultMaxAge)
	maxBuilds := pool.Settings.MaxBuilds
	if maxBuilds == 0 {
		maxBuilds = defaultMaxBuilds
	}
	var errs []error
	retire := func(m *Machine, reason string) {
		m.State, m.Reason = MachineRetiring, reason
		if uerr := ms.UpdateMachine(ctx, m); uerr != nil {
			errs = append(errs, uerr)
		}
	}
	// What the cloud runs for this pool, so a create that answered late is
	// found and one Loomplane never recorded is removed.
	listed, lerr := s.Provisioner.List(ctx, pool.OrgID, pool.Settings.CloudConnectionID, pool.Settings.Region, poolTag(pool.ID))
	if lerr != nil {
		errs = append(errs, lerr)
	}
	known := map[string]bool{}
	warmLive, live := 0, 0
	for i := range machines {
		m := &machines[i]
		age := now.Sub(m.CreatedAt)
		switch m.State {
		case MachineCreating:
			if m.ProviderID == "" && lerr == nil {
				for pid, name := range listed {
					if name == m.Name {
						m.ProviderID, m.State, m.Reason = pid, MachineBooting, ""
						if uerr := ms.UpdateMachine(ctx, m); uerr != nil {
							errs = append(errs, uerr)
						}
					}
				}
			}
			if m.State == MachineCreating && age > bootDeadline {
				retire(m, "the cloud never confirmed the machine")
			}
		case MachineBooting:
			if age > bootDeadline {
				retire(m, "its runner never joined; check the toolchain bundle and the machine's console")
			}
		case MachineReady:
			n, nerr := s.Nodes.Get(ctx, m.OrgID, m.NodeID)
			switch {
			case nerr == nil && n.Status == nodes.StatusRevoked:
				retire(m, "its runner was revoked")
			case (nerr != nil || n.Status != nodes.StatusOnline) && now.Sub(m.ActiveAt) > offlineGrace:
				retire(m, "its runner went offline")
			case !m.Warm && m.Builds >= 1 && m.InFlight == 0:
				retire(m, "a cold runner takes one build")
			case m.Warm && m.InFlight == 0 && (m.Builds >= maxBuilds || age > maxAge):
				retire(m, "a warm runner is replaced after its builds or its age")
			case !m.Warm && m.InFlight == 0 && m.Builds == 0 && now.Sub(m.ActiveAt) > idle:
				retire(m, "no build came for it")
			}
		}
		if m.ProviderID != "" {
			known[m.ProviderID] = true
		}
		if m.State != MachineRetiring {
			live++
			if m.Warm {
				warmLive++
			}
		}
	}
	// Keep the warm runners the pool wants, one more each minute.
	if warmLive < pool.Settings.MinWarm && live < pool.Settings.MaxRunners {
		if serr := s.startMachine(ctx, ms, pool, true); serr != nil {
			errs = append(errs, serr)
		}
	}
	for i := range machines {
		m := &machines[i]
		if m.State != MachineRetiring {
			continue
		}
		if m.ProviderID != "" {
			if derr := s.Provisioner.Delete(ctx, pool.OrgID, pool.Settings.CloudConnectionID, pool.Settings.Region, m.ProviderID); derr != nil {
				errs = append(errs, derr)
				continue
			}
		}
		if m.NodeID != "" && s.Revoker != nil {
			if rerr := s.Revoker.RevokeRunner(ctx, m.OrgID, m.NodeID); rerr != nil && !errors.Is(rerr, nodes.ErrNotFound) {
				errs = append(errs, rerr)
			}
		}
		gone := now
		m.State, m.GoneAt = MachineGone, &gone
		if uerr := ms.UpdateMachine(ctx, m); uerr != nil {
			errs = append(errs, uerr)
		}
	}
	// A machine the cloud runs for this pool that no record names, or that
	// a record retired already, is deleted.
	if lerr == nil {
		for pid, name := range listed {
			if known[pid] || !machineName.MatchString(name) {
				continue
			}
			if derr := s.Provisioner.Delete(ctx, pool.OrgID, pool.Settings.CloudConnectionID, pool.Settings.Region, pid); derr != nil {
				errs = append(errs, derr)
			}
		}
	}
	return errors.Join(errs...)
}

func minutesOr(minutes int, fallback time.Duration) time.Duration {
	if minutes > 0 {
		return time.Duration(minutes) * time.Minute
	}
	return fallback
}

// linkMachine binds a runner that joined with a machine's token to that
// machine, and marks it ready.
func (s *Service) linkMachine(ctx context.Context, n *nodes.Node, pool *Pool) error {
	id := n.Labels[MachineLabel]
	if id == "" || pool.Mode != ModeOnDemand {
		return nil
	}
	ms, err := s.machines()
	if err != nil {
		return err
	}
	machines, err := ms.ListMachines(ctx, pool.ID)
	if err != nil {
		return err
	}
	for i := range machines {
		m := &machines[i]
		if m.ID != id || m.State == MachineRetiring {
			continue
		}
		if m.NodeID != "" && m.NodeID != n.ID {
			return fmt.Errorf("machine %s already has runner %s", m.ID, m.NodeID)
		}
		now := s.now()
		if m.JoinedAt == nil {
			m.JoinedAt = &now
		}
		m.NodeID, m.State, m.ActiveAt, m.Reason = n.ID, MachineReady, now, ""
		return ms.UpdateMachine(ctx, m)
	}
	return nil
}

// BuildStarted counts a build starting on a runner. A cold on-demand
// runner takes no other.
func (s *Service) BuildStarted(ctx context.Context, _, nodeID string) error {
	ms, err := s.machines()
	if err != nil {
		return nil //nolint:nilerr // a store without machines has no on-demand runner to count
	}
	return ms.AdjustInFlight(ctx, nodeID, 1, false, s.now())
}

// BuildEnded counts a build ending on a runner, whether it worked or not.
func (s *Service) BuildEnded(ctx context.Context, _, nodeID string) error {
	ms, err := s.machines()
	if err != nil {
		return nil //nolint:nilerr // as BuildStarted
	}
	return ms.AdjustInFlight(ctx, nodeID, -1, true, s.now())
}
