package notifications

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"

	"github.com/xraph/herald"
	"github.com/xraph/herald/credential"
	"github.com/xraph/herald/driver/push"
	"github.com/xraph/herald/provider"

	"github.com/xraph/loomplane/internal/security"
)

// ErrPushDisabled is returned by push operations when the operator turned push
// off.
var ErrPushDisabled = errors.New("push notifications are turned off on this server")

const pushProviderName = "loomplane-push"

// PushConfig is what the app needs to register a phone: whether push is on,
// where the gateway is, and the key this server signs requests with.
type PushConfig struct {
	Enabled    bool   `json:"enabled"`
	GatewayURL string `json:"gateway_url"`
	ServerKey  string `json:"server_key"` // standard base64 Ed25519 public key
	KeyID      string `json:"key_id"`
}

// serverKey derives the Ed25519 key that signs gateway requests. It follows
// secret_key, so rotating the secret rotates the key.
func serverKey(secretKey string) (ed25519.PrivateKey, error) {
	seed, err := security.DeriveKey(secretKey, "loomplane/push gateway v1")
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// KeyID names a public key: hex of the first 8 bytes of its SHA-256.
func KeyID(public []byte) string { return push.KeyID(public) }

func (s *Service) PushEnabled() bool { return s.cfg.Push.Enabled }

// PushConfig answers any signed-in person. With push off it carries no
// gateway or key.
func (s *Service) PushConfig(_ context.Context, p security.Principal) (PushConfig, error) {
	if p.UserID == "" {
		return PushConfig{}, security.ErrUnauthenticated
	}
	if !s.PushEnabled() {
		return PushConfig{}, nil
	}
	key, err := serverKey(s.cfg.SecretKey)
	if err != nil {
		return PushConfig{}, err
	}
	public := key.Public().(ed25519.PublicKey) //nolint:errcheck // an ed25519 private key always has an ed25519 public key
	return PushConfig{Enabled: true, GatewayURL: s.cfg.Push.GatewayURL, ServerKey: base64.StdEncoding.EncodeToString(public), KeyID: KeyID(public)}, nil
}

// preparePush keeps one sealed push provider in step with the config. With
// push off and no provider yet it creates nothing.
func (s *Service) preparePush(ctx context.Context) error {
	key, err := serverKey(s.cfg.SecretKey)
	if err != nil {
		return err
	}
	creds := map[string]string{"signing_key": base64.StdEncoding.EncodeToString(key.Seed())}
	settings := map[string]string{"gateway_url": s.cfg.Push.GatewayURL}
	appID := s.cfg.Identity.AppID
	list, err := s.mailStore.ListAllProviders(ctx, appID)
	if err != nil {
		return err
	}
	enabled := s.cfg.Push.Enabled
	for _, p := range list {
		if p.Name != pushProviderName {
			continue
		}
		_, err = s.herald.UpdateProvider(ctx, appID, p.ID, herald.ProviderUpdate{SetCredentials: creds, SetSettings: settings, Enabled: &enabled})
		if !errors.Is(err, credential.ErrMalformed) && !errors.Is(err, herald.ErrCredentialKeyUnavailable) {
			return err
		}
		// A rotated secret_key also rotates the key that seals stored
		// credentials, so the old signing key can't be read back. It is
		// replaced whole, not merged.
		if err := s.herald.DeleteProvider(ctx, appID, p.ID); err != nil {
			return err
		}
		break
	}
	if !enabled {
		return nil
	}
	return s.herald.CreateProvider(ctx, &provider.Provider{AppID: appID, Name: pushProviderName, Channel: "push", Driver: "sealed", Credentials: creds, Settings: settings, Enabled: true})
}
