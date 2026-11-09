package notifications

import (
	"errors"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	heraldcontract "github.com/xraph/herald/extension/contract"
	relaycontract "github.com/xraph/relay/extension/contract"
)

// RegisterContract registers Herald's and Relay's dashboard contributors over
// the engines this service runs, as their own Forge extensions wire them, for
// the instance console. Herald answers for Loomplane's app, where the mail
// provider and templates live.
//
// What keeps secrets out differs. Herald reads through privateHeraldStore,
// which never stores a message's recipient, subject or body, and its
// provider reads report how each credential is protected, never its value.
// Relay's store decrypts a webhook's signing secret on every read
// (encryptedRelayStore), so the secret is in memory; only Relay's endpoint
// response types, which have no field for it, keep it out of the console.
// The console integration test checks both.
func (s *Service) RegisterContract(disp *dispatcher.Dispatcher, reg contract.Registry, wreg contract.WardenRegistry, logger forge.Logger) error {
	var errs []error
	if err := heraldcontract.Register(disp, reg, wreg, heraldcontract.Deps{Herald: s.herald, DefaultAppID: s.cfg.Identity.AppID, Logger: logger}); err != nil {
		errs = append(errs, fmt.Errorf("herald contract: %w", err))
	}
	if err := relaycontract.Register(disp, reg, wreg, relaycontract.Deps{Relay: s.relay}); err != nil {
		errs = append(errs, fmt.Errorf("relay contract: %w", err))
	}
	return errors.Join(errs...)
}
