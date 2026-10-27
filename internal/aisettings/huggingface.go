package aisettings

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xraph/vault/secret"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/security"
)

// huggingFaceTokenID names the token's Vault key: "<org id>/huggingface-token".
const huggingFaceTokenID = "huggingface-token"

// huggingFaceTokenPattern is what a Hugging Face token looks like: "hf_" and
// then printable ASCII with no spaces. Hugging Face itself decides whether
// it's real.
var huggingFaceTokenPattern = regexp.MustCompile(`^hf_[\x21-\x7e]{8,250}$`)

// TokenChecker asks Hugging Face whose a token is. *hfhub.Client is the
// real one.
type TokenChecker interface {
	WhoAmI(ctx context.Context, token string) (string, error)
}

// GatedModels lists the workspace's model deployments that download gated
// weights with its Hugging Face token, by label ("llama-8b in
// shop/production"). The models service is the real one.
type GatedModels interface {
	GatedDeployments(ctx context.Context, orgID string) ([]string, error)
}

// HuggingFaceTokens keeps the value of each workspace's Hugging Face token.
type HuggingFaceTokens interface {
	// Put replaces the workspace's token.
	Put(ctx context.Context, orgID, token string) error
	// Reveal returns the workspace's token, or ErrNotFound.
	Reveal(ctx context.Context, orgID string) (string, error)
	// Delete removes the workspace's token. One that's already gone is not
	// an error.
	Delete(ctx context.Context, orgID string) error
}

// VaultHuggingFaceTokens keeps each workspace's Hugging Face token in
// Vault, under VaultAppID beside the provider keys, at
// "<org id>/huggingface-token".
type VaultHuggingFaceTokens struct {
	secrets *connections.VaultSecrets
}

// NewVaultHuggingFaceTokens returns VaultHuggingFaceTokens over a Vault
// secret service built for VaultAppID.
func NewVaultHuggingFaceTokens(svc *secret.Service) *VaultHuggingFaceTokens {
	return &VaultHuggingFaceTokens{secrets: connections.NewVaultSecretsFor(svc, VaultAppID)}
}

var _ HuggingFaceTokens = (*VaultHuggingFaceTokens)(nil)

// Put implements HuggingFaceTokens.
func (v *VaultHuggingFaceTokens) Put(ctx context.Context, orgID, token string) error {
	if err := v.secrets.Put(ctx, orgID, huggingFaceTokenID, []byte(token)); err != nil {
		return fmt.Errorf("store the Hugging Face token: %w", err)
	}
	return nil
}

// Reveal implements HuggingFaceTokens.
func (v *VaultHuggingFaceTokens) Reveal(ctx context.Context, orgID string) (string, error) {
	b, err := v.secrets.Reveal(ctx, orgID, huggingFaceTokenID)
	if errors.Is(err, connections.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read the Hugging Face token: %w", err)
	}
	return string(b), nil
}

// Delete implements HuggingFaceTokens.
func (v *VaultHuggingFaceTokens) Delete(ctx context.Context, orgID string) error {
	if err := v.secrets.Delete(ctx, orgID, huggingFaceTokenID); err != nil {
		return fmt.Errorf("delete the Hugging Face token: %w", err)
	}
	return nil
}

// SetTokenChecker binds what checks a Hugging Face token before it's saved.
func (s *Service) SetTokenChecker(c TokenChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checker = c
}

// SetGatedModels binds what lists the deployments that need the token, once
// the models service is built. Until then a delete goes ahead.
func (s *Service) SetGatedModels(g GatedModels) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gated = g
}

func (s *Service) tokenChecker() TokenChecker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checker
}

func (s *Service) gatedModels() GatedModels {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gated
}

// huggingFaceHint is what the dashboard shows of a token: "hf_", an
// ellipsis and the last four characters.
func huggingFaceHint(token string) string {
	return "hf_…" + token[len(token)-4:]
}

// withoutToken returns err, or, when its text holds the token, an error with
// the token taken out that wraps nothing, so no unwrapping finds it.
func withoutToken(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(hfhub.Redact(err.Error(), token))
}

// PutHuggingFaceToken checks token with Hugging Face and saves it as the
// workspace's, replacing any other. ai:manage. The value goes to Vault
// before the metadata is saved, and nothing returned, logged or audited
// ever holds it.
func (s *Service) PutHuggingFaceToken(ctx context.Context, p security.Principal, token string) (*HuggingFaceToken, error) {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return nil, err
	}
	if !huggingFaceTokenPattern.MatchString(token) {
		return nil, invalid("a Hugging Face token starts with hf_. Create one with read access at huggingface.co/settings/tokens")
	}
	checker := s.tokenChecker()
	if checker == nil || s.hfTokens == nil {
		return nil, errors.New("aisettings: Hugging Face tokens aren't configured")
	}
	account, err := checker.WhoAmI(ctx, token)
	switch {
	case errors.Is(err, hfhub.ErrUnauthorized):
		return nil, invalid("Hugging Face didn't accept this token.")
	case errors.Is(err, hfhub.ErrUnavailable):
		return nil, fmt.Errorf("check the Hugging Face token: %w", hfhub.ErrUnavailable)
	case err != nil:
		return nil, withoutToken(fmt.Errorf("check the Hugging Face token: %w", err), token)
	}
	if perr := s.hfTokens.Put(ctx, p.OrgID, token); perr != nil {
		return nil, withoutToken(perr, token)
	}
	hf, err := s.store.SetHuggingFaceToken(ctx, p.OrgID, huggingFaceHint(token), account)
	if err != nil {
		return nil, fmt.Errorf("the Hugging Face token is in Vault, but its details weren't saved: %w", err)
	}
	s.record(ctx, p, "ai.huggingface_token.put", "", map[string]any{"account": hf.Account, "revision": hf.Revision})
	return hf, nil
}

// DeleteHuggingFaceToken deletes the workspace's Hugging Face token.
// ai:manage. ErrInUse, naming them, while gated models that download with
// it are deployed. Deleting when there's none is not an error.
func (s *Service) DeleteHuggingFaceToken(ctx context.Context, p security.Principal) error {
	if err := s.authz.Require(ctx, p, security.AIManage, ""); err != nil {
		return err
	}
	if s.hfTokens == nil {
		return errors.New("aisettings: Hugging Face tokens aren't configured")
	}
	st, err := s.store.Settings(ctx, p.OrgID)
	if err != nil {
		return err
	}
	if st.HuggingFace == nil {
		// Nothing points at a value, but one may be left from a delete that
		// failed halfway.
		return s.hfTokens.Delete(ctx, p.OrgID)
	}
	if g := s.gatedModels(); g != nil {
		using, err := g.GatedDeployments(ctx, p.OrgID)
		if err != nil {
			return fmt.Errorf("check which models use the Hugging Face token: %w", err)
		}
		if len(using) > 0 {
			return &tokenInUseError{models: using}
		}
	}
	if err := s.store.ClearHuggingFaceToken(ctx, p.OrgID); err != nil {
		return err
	}
	s.record(ctx, p, "ai.huggingface_token.delete", "", map[string]any{"account": st.HuggingFace.Account})
	if err := s.hfTokens.Delete(ctx, p.OrgID); err != nil {
		return fmt.Errorf("the Hugging Face token is deleted, but its value is still in Vault: %w", err)
	}
	return nil
}

// tokenInUseError is ErrInUse for the Hugging Face token, with its own
// sentence: ErrInUse's text is about provider keys.
type tokenInUseError struct{ models []string }

func (e *tokenInUseError) Error() string {
	return "gated models download with the workspace's Hugging Face token: " + strings.Join(e.models, ", ") +
		". Remove them first, or put a new token in its place"
}

func (e *tokenInUseError) Unwrap() error { return ErrInUse }

// HuggingFaceTokenValue returns the workspace's Hugging Face token and its
// revision, or ErrNotFound when none is set. For the models service only:
// never serialize, log or audit the value.
func (s *Service) HuggingFaceTokenValue(ctx context.Context, orgID string) (token string, revision int64, err error) {
	if s.hfTokens == nil {
		return "", 0, ErrNotFound
	}
	st, err := s.store.Settings(ctx, orgID)
	if err != nil {
		return "", 0, err
	}
	if st.HuggingFace == nil {
		return "", 0, ErrNotFound
	}
	token, err = s.hfTokens.Reveal(ctx, orgID)
	if err != nil {
		return "", 0, err
	}
	return token, st.HuggingFace.Revision, nil
}
