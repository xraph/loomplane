package identity

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"

	"github.com/xraph/chronicle/checkpoint"
	"github.com/xraph/forge"
	"github.com/xraph/grove"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"

	"github.com/xraph/loomplane/internal/checkpoints"
	"github.com/xraph/loomplane/internal/security"
)

// auditCheckpoints signs the audit trail's streams on every API replica.
// The worker builds no identity extension, so it signs nothing.
type auditCheckpoints struct {
	keys   *checkpoints.Keys
	signer *checkpoint.Ed25519Signer
	// sched is nil when chronicle.checkpoints.enabled is off.
	sched *checkpoints.Scheduler

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newAuditCheckpoints builds the Vault key provider and the signer. It
// reads nothing: the key is created or loaded once Start runs.
func (e *Extension) newAuditCheckpoints(db *grove.DB) (*auditCheckpoints, error) {
	key, err := security.DeriveKey(e.cfg.SecretKey, checkpoints.KeyLabel)
	if err != nil {
		return nil, err
	}
	enc, err := crypto.NewEncryptor(key)
	if err != nil {
		return nil, fmt.Errorf("checkpoint key encryptor: %w", err)
	}
	k := checkpoints.NewKeys(secret.NewService(vaultpg.New(db), enc, secret.WithAppID(checkpoints.VaultAppID)))
	return &auditCheckpoints{keys: k, signer: checkpoint.NewEd25519Signer(k)}, nil
}

// scheduleCheckpoints sets up the ticker when checkpoints are on. Call it once
// e.auditStore exists.
func (e *Extension) scheduleCheckpoints(fapp forge.App) {
	cfg := e.cfg.Chronicle.Checkpoints
	if !cfg.Enabled {
		return
	}
	taker := checkpoint.NewCheckpointer(e.auditStore, e.checkpoints.signer, e.Logger(), checkpoint.WithMetrics(fapp.Metrics()))
	e.checkpoints.sched = checkpoints.NewScheduler(e.auditStore, taker,
		checkpoints.Policy{EveryEvents: cfg.EveryEvents, EveryInterval: cfg.EveryInterval}, e.Logger())
}

// ensureCheckpointKey creates the signing key when Vault has none. prepare
// runs it under the startup lock, so replicas starting together agree on
// one key.
func (e *Extension) ensureCheckpointKey(ctx context.Context) error {
	if e.checkpoints == nil || e.checkpoints.sched == nil {
		return nil
	}
	created, err := e.checkpoints.keys.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("checkpoint signing key: %w", err)
	}
	if created {
		e.Logger().Info("created the audit checkpoint signing key in Vault", forge.F("vault_app", checkpoints.VaultAppID))
	}
	return nil
}

// startCheckpoints logs the public key, for an admin to pin in Hallproof,
// and starts the ticker. Nothing happens when checkpoints are off.
func (e *Extension) startCheckpoints(ctx context.Context) error {
	if e.checkpoints == nil || e.checkpoints.sched == nil {
		return nil
	}
	pub, keyID, err := e.checkpoints.keys.PublicKey(ctx)
	if err != nil {
		return fmt.Errorf("checkpoint signing key: %w", err)
	}
	cfg := e.cfg.Chronicle.Checkpoints
	e.Logger().Info("audit checkpoints are signed",
		forge.F("key_id", keyID),
		forge.F("public_key", base64.StdEncoding.EncodeToString(pub)),
		forge.F("algorithm", checkpoint.AlgorithmEd25519),
		forge.F("every_events", cfg.EveryEvents),
		forge.F("every_interval", cfg.EveryInterval.String()),
	)
	// The ticker outlives Start's context, which ends when the app has
	// started; Stop cancels it.
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.checkpoints.cancel = cancel
	e.checkpoints.wg.Go(func() { e.checkpoints.sched.Run(run) })
	return nil
}

// stopCheckpoints stops the ticker and waits for a pass in flight.
func (e *Extension) stopCheckpoints() {
	if e.checkpoints == nil || e.checkpoints.cancel == nil {
		return
	}
	e.checkpoints.cancel()
	e.checkpoints.wg.Wait()
	e.checkpoints.cancel = nil
}
