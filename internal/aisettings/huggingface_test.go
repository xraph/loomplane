package aisettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultmem "github.com/xraph/vault/store/memory"

	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/security"
)

// hfToken looks like a Hugging Face token and is nobody's.
const hfToken = "hf_testTOKENvalueabcdwxyz"

// fakeChecker answers WhoAmI with account, or err, and counts the calls.
type fakeChecker struct {
	mu      sync.Mutex
	account string
	err     error
	calls   int
}

func (f *fakeChecker) WhoAmI(_ context.Context, token string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if !strings.HasPrefix(token, "hf_") {
		return "", hfhub.ErrUnauthorized
	}
	return f.account, nil
}

// fakeGated lists the deployments a test says use the token.
type fakeGated struct {
	models []string
	err    error
}

func (f *fakeGated) GatedDeployments(_ context.Context, orgID string) ([]string, error) {
	if orgID != "org" {
		return nil, nil
	}
	return f.models, f.err
}

// failingTokens is a HuggingFaceTokens whose writes fail, quoting the value
// they were given, as a careless backend might.
type failingTokens struct{ HuggingFaceTokens }

func (failingTokens) Put(_ context.Context, _, token string) error {
	return fmt.Errorf("vault is sealed, couldn't write %s", token)
}

type hfHarness struct {
	*harness
	vault   *secret.Service
	tokens  HuggingFaceTokens
	checker *fakeChecker
	gated   *fakeGated
}

// newHFHarness is a Service with real Vault token storage over Vault's
// in-memory store, a fake token checker and a fake gated-model list.
func newHFHarness(t *testing.T) *hfHarness {
	t.Helper()
	enc, err := crypto.NewEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	vault := secret.NewService(vaultmem.New(), enc, secret.WithAppID(VaultAppID))
	h := &hfHarness{
		harness: newHarness(t), vault: vault, tokens: NewVaultHuggingFaceTokens(vault),
		checker: &fakeChecker{account: "rex"}, gated: &fakeGated{},
	}
	h.rebuild()
	return h
}

func (h *hfHarness) rebuild() {
	h.svc = NewService(Deps{
		Store: h.store, Keys: h.keys, HuggingFace: h.tokens, Authz: roleAuthz{}, Audit: h.audit, Projects: memProjects{},
		Notify: h.notify, Now: func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
	})
	h.svc.SetTokenChecker(h.checker)
	h.svc.SetGatedModels(h.gated)
}

func TestPutHuggingFaceToken(t *testing.T) {
	ctx := context.Background()
	h := newHFHarness(t)

	got, err := h.svc.PutHuggingFaceToken(ctx, admin, hfToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hint != "hf_…wxyz" || got.Account != "rex" || got.Revision != 1 || got.SetAt.IsZero() {
		t.Errorf("token = %+v", got)
	}
	// The value is in Vault at <org>/huggingface-token, under the AI app.
	sec, err := h.vault.Get(ctx, "org/huggingface-token", VaultAppID)
	if err != nil || string(sec.Value) != hfToken {
		t.Fatalf("vault value: %v", err)
	}
	if v, gerr := h.svc.Get(ctx, viewer); gerr != nil || v.HuggingFace == nil || v.HuggingFace.Hint != "hf_…wxyz" {
		t.Errorf("Get = %+v, %v", v, gerr)
	}

	// A second put replaces it and bumps the revision.
	if got, err = h.svc.PutHuggingFaceToken(ctx, admin, "hf_anotherTOKENvalue9876"); err != nil || got.Revision != 2 || got.Hint != "hf_…9876" {
		t.Errorf("second put = %+v, %v", got, err)
	}

	calls := h.checker.calls
	_, err = h.svc.PutHuggingFaceToken(ctx, developer, hfToken)
	mustErr(t, err, security.ErrForbidden, "")
	for _, bad := range []string{"abc", "", "hf_short", "sk-" + hfToken[3:], "hf_has a space in it", "hf_line\nbreak123"} {
		_, err = h.svc.PutHuggingFaceToken(ctx, admin, bad)
		mustErr(t, err, ErrInvalid, "hf_")
	}
	if h.checker.calls != calls {
		t.Errorf("Hugging Face was asked about %d refused tokens", h.checker.calls-calls)
	}

	h.checker.err = fmt.Errorf("%w: 401", hfhub.ErrUnauthorized)
	_, err = h.svc.PutHuggingFaceToken(ctx, admin, "hf_rejectedTOKEN1234")
	mustErr(t, err, ErrInvalid, "Hugging Face didn't accept this token.")
	h.checker.err = fmt.Errorf("%w: 503", hfhub.ErrUnavailable)
	_, err = h.svc.PutHuggingFaceToken(ctx, admin, "hf_unreachedTOKEN1234")
	mustErr(t, err, hfhub.ErrUnavailable, "")
	h.checker.err = nil

	// A failed Vault write saves no metadata: the value goes first.
	h.tokens = failingTokens{h.tokens}
	h.rebuild()
	if _, err = h.svc.PutHuggingFaceToken(ctx, admin, "hf_unsavedTOKEN12345"); err == nil {
		t.Fatal("put with Vault failing: no error")
	}
	if v, _ := h.svc.Get(ctx, viewer); v.HuggingFace == nil || v.HuggingFace.Revision != 2 {
		t.Errorf("after a failed Vault write the token is %+v, want revision 2 kept", v.HuggingFace)
	}

	var puts []map[string]any
	for _, e := range h.audit.all() {
		if e.Action == "ai.huggingface_token.put" {
			puts = append(puts, e.Attributes)
		}
	}
	if len(puts) != 2 || puts[0]["account"] != "rex" {
		t.Errorf("audited puts %v, want the two that succeeded", puts)
	}
}

func TestHuggingFaceTokenNeverReturned(t *testing.T) {
	ctx := context.Background()
	h := newHFHarness(t)
	errs := make([]error, 0, 3)

	hf, err := h.svc.PutHuggingFaceToken(ctx, admin, hfToken)
	if err != nil {
		t.Fatal(err)
	}
	// A checker or a store that quotes the token in its error.
	h.checker.err = fmt.Errorf("whoami for %s: boom", hfToken)
	_, err = h.svc.PutHuggingFaceToken(ctx, admin, hfToken)
	errs = append(errs, err)
	h.checker.err = nil
	h.tokens = failingTokens{h.tokens}
	h.rebuild()
	_, err = h.svc.PutHuggingFaceToken(ctx, admin, hfToken)
	errs = append(errs, err)
	h.gated.models = []string{"llama-8b in shop/production"}
	errs = append(errs, h.svc.DeleteHuggingFaceToken(ctx, admin))

	views := make([]any, 0, 3)
	views = append(views, hf)
	for _, p := range []security.Principal{viewer, admin} {
		v, err := h.svc.Get(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		views = append(views, v)
	}
	for _, v := range views {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), hfToken) || strings.Contains(fmt.Sprintf("%v %+v %#v", v, v, v), hfToken) {
			t.Errorf("an answer holds the token: %s", b)
		}
		if !strings.Contains(string(b), `"hint":"hf_…wxyz"`) {
			t.Errorf("an answer lacks the hint: %s", b)
		}
	}
	for _, e := range h.audit.all() {
		if strings.Contains(fmt.Sprintf("%v %+v", e.Attributes, e), hfToken) {
			t.Errorf("audit event %s holds the token", e.Action)
		}
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("error %d: none", i)
		}
		for e := err; e != nil; e = errors.Unwrap(e) {
			if strings.Contains(e.Error(), hfToken) {
				t.Errorf("error %d holds the token: %q", i, e)
			}
		}
	}
}

func TestDeleteTokenUsedByGatedModels(t *testing.T) {
	ctx := context.Background()
	h := newHFHarness(t)
	if _, err := h.svc.PutHuggingFaceToken(ctx, admin, hfToken); err != nil {
		t.Fatal(err)
	}

	h.gated.models = []string{"llama-8b in shop/production"}
	err := h.svc.DeleteHuggingFaceToken(ctx, admin)
	mustErr(t, err, ErrInUse, "llama-8b in shop/production")
	if strings.Contains(err.Error(), "provider key") {
		t.Errorf("the refusal talks about provider keys: %q", err)
	}
	if v, rev, err := h.svc.HuggingFaceTokenValue(ctx, "org"); err != nil || v != hfToken || rev != 1 {
		t.Errorf("after a refused delete: %v, revision %d", err, rev)
	}
	mustErr(t, h.svc.DeleteHuggingFaceToken(ctx, developer), security.ErrForbidden, "")

	h.gated.err = errors.New("models service down")
	h.gated.models = nil
	if err := h.svc.DeleteHuggingFaceToken(ctx, admin); err == nil {
		t.Error("delete with the gated-model check failing: no error")
	}
	h.gated.err = nil

	if err := h.svc.DeleteHuggingFaceToken(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.HuggingFaceTokenValue(ctx, "org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("value after delete: %v, want ErrNotFound", err)
	}
	if _, err := h.vault.Get(ctx, "org/huggingface-token", VaultAppID); err == nil {
		t.Error("the value is still in Vault")
	}
	if v, _ := h.svc.Get(ctx, viewer); v.HuggingFace != nil {
		t.Errorf("Get after delete shows %+v", v.HuggingFace)
	}
	// Deleting again is fine, and a new token never reuses a revision.
	if err := h.svc.DeleteHuggingFaceToken(ctx, admin); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if hf, err := h.svc.PutHuggingFaceToken(ctx, admin, hfToken); err != nil || hf.Revision != 3 {
		t.Errorf("put after delete = %+v, %v; want revision 3", hf, err)
	}

	deletes := 0
	for _, e := range h.audit.all() {
		if e.Action == "ai.huggingface_token.delete" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Errorf("audited %d deletes, want 1", deletes)
	}
}

func TestHuggingFaceTokenValue(t *testing.T) {
	ctx := context.Background()
	h := newHFHarness(t)
	if _, _, err := h.svc.HuggingFaceTokenValue(ctx, "org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("before any put: %v, want ErrNotFound", err)
	}
	if _, err := h.svc.PutHuggingFaceToken(ctx, admin, hfToken); err != nil {
		t.Fatal(err)
	}
	if v, rev, err := h.svc.HuggingFaceTokenValue(ctx, "org"); err != nil || v != hfToken || rev != 1 {
		t.Errorf("value = revision %d, %v", rev, err)
	}
	if _, _, err := h.svc.HuggingFaceTokenValue(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace: %v, want ErrNotFound", err)
	}
	// A value left in Vault with no metadata pointing at it isn't served.
	if err := h.store.ClearHuggingFaceToken(ctx, "org"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.HuggingFaceTokenValue(ctx, "org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("value with the metadata cleared: %v, want ErrNotFound", err)
	}
}

func TestHuggingFaceTokenNeedsItsDependencies(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t) // no token store, no checker
	if _, err := h.svc.PutHuggingFaceToken(ctx, admin, hfToken); err == nil {
		t.Error("put with nothing configured: no error")
	}
	if _, _, err := h.svc.HuggingFaceTokenValue(ctx, "org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("value with nothing configured: %v, want ErrNotFound", err)
	}
}

func TestVaultHuggingFaceTokensKeepOnePerWorkspace(t *testing.T) {
	ctx := context.Background()
	h := newHFHarness(t)
	tokens := h.tokens
	if _, err := tokens.Reveal(ctx, "org"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reveal before Put = %v, want ErrNotFound", err)
	}
	if err := tokens.Put(ctx, "org", hfToken); err != nil {
		t.Fatal(err)
	}
	if got, err := tokens.Reveal(ctx, "org"); err != nil || got != hfToken {
		t.Fatalf("Reveal: %v", err)
	}
	if _, err := tokens.Reveal(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another workspace read the token: %v", err)
	}
	// The provider key app's other keys are untouched by the token's.
	if _, err := h.vault.Get(ctx, "org/provider-keys/huggingface-token", VaultAppID); err == nil {
		t.Error("the token landed among the provider keys")
	}
	if err := tokens.Delete(ctx, "org"); err != nil {
		t.Fatal(err)
	}
	if err := tokens.Delete(ctx, "org"); err != nil {
		t.Errorf("second Delete = %v", err)
	}
}
