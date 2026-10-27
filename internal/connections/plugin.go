package connections

import (
	"bytes"
	"context"
	"fmt"
	"maps"

	"github.com/xraph/loomplane/internal/plugins"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

// PluginChecker checks credentials for a provider plugin's cloud by asking
// the plugin. The settings you chose are kept with the details it returns,
// so every deployment gets them back.
type PluginChecker struct{ Client *plugins.Client }

// IsGrant implements GrantChecker.
func (PluginChecker) IsGrant(credential []byte) bool { return keylessJSON(credential) }

// Parse implements Checker: the plugin decides what a credential looks
// like, so all Loomplane can check is that there is one.
func (p PluginChecker) Parse(credential []byte, settings map[string]string) (Check, error) {
	if len(bytes.TrimSpace(credential)) == 0 {
		return Check{}, fmt.Errorf("%w: the credential is empty", ErrInvalid)
	}
	return Check{Details: maps.Clone(settings)}, nil
}

// Check implements Checker.
func (p PluginChecker) Check(ctx context.Context, credential []byte, settings map[string]string) (Check, error) {
	a, err := p.Client.Check(ctx, sdk.Credential{Secret: credential, Settings: settings})
	if err != nil {
		return Check{}, err
	}
	details := maps.Clone(settings)
	if details == nil {
		details = map[string]string{}
	}
	maps.Copy(details, a.Details)
	return Check{Account: a.Account, Details: details, Missing: a.Missing}, nil
}
