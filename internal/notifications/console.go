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
// provider and templates live. Both read through the stores the service
// wraps, so a message's body and a webhook's signing secret stay out of
// what they return.
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
