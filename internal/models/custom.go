package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/aisettings"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/security"
)

// errNoToken is what Tokens returns when the workspace has no Hugging Face
// token.
var errNoToken = aisettings.ErrNotFound

// CustomStore keeps the entries a workspace built from pasted Hugging Face
// repos, once one is deployed. Every read and write is scoped to an
// organization: another organization's entry reads as ErrEntryNotFound.
type CustomStore interface {
	engines.CustomEntries
	// PutCustom stores e for orgID. Its ID pins its repo and revision, so
	// storing one that's already there leaves the stored one alone.
	PutCustom(ctx context.Context, orgID string, e registry.Entry) error
	// ListCustom lists orgID's custom entries by ID.
	ListCustom(ctx context.Context, orgID string) ([]registry.Entry, error)
}

var _ CustomStore = (*PgStore)(nil)

// customIDPattern is a custom entry's ID: "hf:<repo>@<sha12>".
var customIDPattern = regexp.MustCompile(`^hf:[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}@[0-9a-f]{12}$`)

// checkCustom refuses an entry that isn't one Build would make: an ID that
// isn't engines.CustomEntryID of its repo and 40-hex revision, one that
// asks to run its repo's code, weights from another repo, extra engine
// arguments, or a GGUF file name Build wouldn't use. Build never gives a
// quantization arguments, so a stored row that has some was edited by hand,
// and they'd reach the engine's command line.
func checkCustom(e registry.Entry) error {
	if !customIDPattern.MatchString(e.ID) {
		return fmt.Errorf("%w: custom entry ID %q isn't hf:<repo>@<12 hex of the revision>", ErrInvalid, e.ID)
	}
	if !revisionPattern.MatchString(e.Revision) || e.ID != engines.CustomEntryID(repoOf(e), e.Revision) {
		return fmt.Errorf("%w: custom entry %s isn't pinned to the revision its ID names", ErrInvalid, e.ID)
	}
	if e.TrustRemoteCode {
		return fmt.Errorf("%w: custom entry %s asks to run its repository's code", ErrInvalid, e.ID)
	}
	for _, q := range e.Quantizations {
		if q.Repo != "" || q.Revision != "" {
			return fmt.Errorf("%w: custom entry %s keeps %s's weights in another repo", ErrInvalid, e.ID, q.ID)
		}
		if len(q.Args) > 0 {
			return fmt.Errorf("%w: custom entry %s gives %s extra engine arguments", ErrInvalid, e.ID, q.ID)
		}
		if q.File != "" && !hfhub.GGUFName(q.File) {
			return fmt.Errorf("%w: custom entry %s names a GGUF file Loomplane doesn't use", ErrInvalid, e.ID)
		}
	}
	return nil
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PutCustom implements CustomStore.
func (s *PgStore) PutCustom(ctx context.Context, orgID string, e registry.Entry) error {
	if orgID == "" {
		return fmt.Errorf("%w: it has no org", ErrInvalid)
	}
	if err := checkCustom(e); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode custom model %s: %w", e.ID, err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_ai_custom_models (org_id, entry_id, entry, created_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT (org_id, entry_id) DO NOTHING`, orgID, e.ID, raw, s.now().UTC())
	if err != nil {
		return storeError("store custom model", err)
	}
	return nil
}

// Custom implements engines.CustomEntries.
func (s *PgStore) Custom(ctx context.Context, orgID, entryID string) (registry.Entry, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT entry FROM loomplane_ai_custom_models WHERE org_id = $1 AND entry_id = $2`,
		orgID, entryID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return registry.Entry{}, fmt.Errorf("%w: the workspace has no custom model %s", ErrEntryNotFound, entryID)
	}
	if err != nil {
		return registry.Entry{}, fmt.Errorf("read custom model: %w", err)
	}
	return decodeCustom(raw, entryID)
}

// ListCustom implements CustomStore.
func (s *PgStore) ListCustom(ctx context.Context, orgID string) ([]registry.Entry, error) {
	rows, err := s.db.Query(ctx, `SELECT entry_id, entry FROM loomplane_ai_custom_models WHERE org_id = $1 ORDER BY entry_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("list custom models: %w", err)
	}
	defer rows.Close() // read-only; Err below reports iteration failures
	out := []registry.Entry{}
	for rows.Next() {
		var (
			id  string
			raw []byte
		)
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("list custom models: %w", err)
		}
		e, err := decodeCustom(raw, id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list custom models: %w", err)
	}
	return out, nil
}

// decodeCustom reads a stored entry and checks it's still one Build would
// make, so a row edited by hand can't run a repo's code.
func decodeCustom(raw []byte, id string) (registry.Entry, error) {
	var e registry.Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return registry.Entry{}, fmt.Errorf("decode custom model %s: %w", id, err)
	}
	if e.ID != id {
		return registry.Entry{}, fmt.Errorf("custom model %s is stored as %s", id, e.ID)
	}
	if err := checkCustom(e); err != nil {
		return registry.Entry{}, err
	}
	return e, nil
}

// CPUCacheProblem says why q can't run with llama.cpp on CPU on
// Kubernetes, or "" when it can. The CPU kinds get a weight cache of a
// fixed engines.MinCacheMB there, and the weights, with room for the
// tokenizer and config, must fit it. Deploying a model-llamacpp resource to
// a cluster refuses one with this sentence.
func CPUCacheProblem(q registry.Quantization) string {
	if q.Engine != registry.EngineLlamaCPP {
		return ""
	}
	need := engines.CacheMB(q.WeightBytes)
	if need <= engines.MinCacheMB {
		return ""
	}
	return fmt.Sprintf("%s needs a %d MB weight cache, and llama.cpp on CPU gets %d MB on Kubernetes. Run it on a GPU instead.",
		weightsName(q), need, engines.MinCacheMB)
}

// weightsName names a quantization's weights: its GGUF file, or its ID.
func weightsName(q registry.Quantization) string {
	if q.File != "" {
		return q.File
	}
	return q.ID
}

type customKey struct{ org, id string }

// customEntry returns one of the workspace's custom entries: a stored one,
// or one it inspected in the last hour.
func (s *Service) customEntry(ctx context.Context, orgID, id string) (registry.Entry, error) {
	if e, ok := s.inspected.get(customKey{orgID, id}); ok {
		return e, nil
	}
	if s.d.Custom == nil {
		return registry.Entry{}, fmt.Errorf("%w: the workspace has no custom model %s", ErrEntryNotFound, id)
	}
	return s.d.Custom.Custom(ctx, orgID, id)
}

// maxConfigBytes caps a config.json. The largest are a few tens of KiB.
const maxConfigBytes = 1 << 20

// Inspect builds an untested entry from any Hugging Face model repo at
// revision, "" for its current main. The entry is pinned to the commit
// SHA, never a branch, and its ID is engines.CustomEntryID. It's kept for
// the workspace for an hour and stored only when it's deployed. ai:edit.
//
// Inspect reads the repo's metadata and config.json, and nothing an LFS
// download serves. It asks anonymously, and uses the workspace's Hugging
// Face token, from AI settings, only for a gated or private repo.
func (s *Service) Inspect(ctx context.Context, p security.Principal, repo, revision string) (*EntryDetail, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIEdit, ""); err != nil {
		return nil, err
	}
	repo = strings.TrimSpace(repo)
	if !hfhub.ValidRepo(repo) {
		return nil, fmt.Errorf("%w: %q isn't a Hugging Face model ID like owner/name", ErrInvalidQuery, repo)
	}
	if s.d.Hub == nil {
		return nil, errors.New("models: Hugging Face isn't configured")
	}
	if revisionPattern.MatchString(revision) {
		if e, ok := s.inspected.get(customKey{p.OrgID, engines.CustomEntryID(repo, revision)}); ok {
			return s.detail(ctx, p, s.view(e)), nil
		}
	}
	src, err := s.open(ctx, p.OrgID, repo, revision)
	if err != nil {
		return nil, err
	}
	config, baseParams, err := s.config(ctx, p.OrgID, src)
	if err != nil {
		return nil, err
	}
	info := *src.info
	if info.Params <= 0 {
		// A GGUF repo's metadata rarely counts parameters, and its base
		// model's safetensors do.
		info.Params = baseParams
	}
	e, err := hfhub.Build(&info, config)
	if err != nil {
		return nil, err
	}
	e.Gated = e.Gated || src.token != ""
	s.inspected.put(customKey{p.OrgID, e.ID}, e)
	return s.detail(ctx, p, s.view(e)), nil
}

// repoAt is a repo resolved to a commit, with the token that reads it: ""
// unless the repo is gated or private.
type repoAt struct {
	info  *hfhub.ModelInfo
	token string
}

// open reads repo's metadata at revision, anonymously first. A gated repo
// needs the workspace's token and its account's access; a private one the
// token.
func (s *Service) open(ctx context.Context, orgID, repo, revision string) (repoAt, error) {
	info, err := s.d.Hub.Model(ctx, repo, revision, "")
	var token string
	if errors.Is(err, hfhub.ErrGated) || errors.Is(err, hfhub.ErrNotFound) {
		// Anonymous requests for a private repo get the same answers.
		var terr error
		if token, terr = s.token(ctx, orgID); terr != nil {
			return repoAt{}, terr
		}
		if token != "" {
			info, err = s.d.Hub.Model(ctx, repo, revision, token)
		}
	}
	switch {
	case errors.Is(err, hfhub.ErrNotFound):
		return repoAt{}, fmt.Errorf("%w: Hugging Face has no model %s at %s, or the workspace's Hugging Face token can't see it",
			ErrEntryNotFound, repo, revisionOrMain(revision))
	case errors.Is(err, hfhub.ErrGated):
		if token == "" {
			return repoAt{}, &GatedError{Title: repo}
		}
		return repoAt{}, gatedAccessError(repo)
	case err != nil:
		return repoAt{}, fmt.Errorf("read %s from Hugging Face: %w", repo, err)
	}
	if !revisionPattern.MatchString(info.SHA) {
		return repoAt{}, fmt.Errorf("the Hub answered for %s without a commit SHA", repo)
	}
	if revisionPattern.MatchString(revision) && info.SHA != revision {
		return repoAt{}, fmt.Errorf("the Hub answered for %s at %s with commit %s", repo, revision, info.SHA)
	}
	if !info.Gated {
		return repoAt{info: info, token: token}, nil
	}
	if token == "" {
		if token, err = s.token(ctx, orgID); err != nil {
			return repoAt{}, err
		}
		if token == "" {
			return repoAt{}, &GatedError{Title: repo}
		}
	}
	err = s.d.Hub.Access(ctx, info.ID, info.SHA, token)
	if errors.Is(err, hfhub.ErrNotFound) {
		// A GGUF repo usually has no config.json to ask about, whoever
		// asks. Ask about a file it has instead.
		if file := firstGGUF(info); file != "" {
			err = s.d.Hub.AccessFile(ctx, info.ID, info.SHA, file, token)
		}
	}
	switch {
	case errors.Is(err, hfhub.ErrGated):
		return repoAt{}, gatedAccessError(repo)
	case err != nil:
		return repoAt{}, fmt.Errorf("check access to %s on Hugging Face: %w", repo, err)
	}
	return repoAt{info: info, token: token}, nil
}

// config reads the repo's config.json. A GGUF repo usually has none, so
// its base model's is read instead, from the "base_model" tag, along with
// the base model's parameter count from its safetensors (0 when the Hub
// doesn't give one).
func (s *Service) config(ctx context.Context, orgID string, src repoAt) (config []byte, baseParams int64, err error) {
	config, err = s.d.Hub.RawFile(ctx, src.info.ID, src.info.SHA, "config.json", src.token, maxConfigBytes)
	if err == nil || !errors.Is(err, hfhub.ErrNotFound) {
		if err != nil {
			return nil, 0, fmt.Errorf("read %s's config.json: %w", src.info.ID, err)
		}
		return config, 0, nil
	}
	base := baseModel(src.info)
	if base == "" || !hasGGUF(src.info) {
		return nil, 0, fmt.Errorf("%w: %s has no config.json, so Loomplane can't work out how much memory it needs", hfhub.ErrRefused, src.info.ID)
	}
	from, err := s.open(ctx, orgID, base, "")
	if err != nil {
		var gated *GatedError
		var access *GatedAccessError
		if errors.As(err, &gated) || errors.As(err, &access) {
			return nil, 0, &BaseGatedError{Repo: src.info.ID, Base: base, NoToken: gated != nil, err: err}
		}
		return nil, 0, fmt.Errorf("%s has no config.json, and reading its base model's failed: %w", src.info.ID, err)
	}
	config, err = s.d.Hub.RawFile(ctx, from.info.ID, from.info.SHA, "config.json", from.token, maxConfigBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("%s has no config.json, and reading its base model %s's failed: %w", src.info.ID, base, err)
	}
	return config, from.info.Params, nil
}

// BaseGatedError is a GGUF repo, public or not, whose base model is gated:
// Loomplane sizes the GGUF from the base model's config.json, and only
// reading that needs the workspace's Hugging Face token. It says so, names
// the base model, and holds nothing secret.
type BaseGatedError struct {
	Repo, Base string
	// NoToken is true when the workspace has no token, false when its
	// token's account hasn't been granted access to Base.
	NoToken bool
	err     error
}

func (e *BaseGatedError) Error() string {
	head := e.Repo + " has no config.json, so Loomplane sizes it from its base model " + e.Base +
		", which is gated on Hugging Face. The workspace's Hugging Face token is needed only for that, to read " + e.Base + "'s config.json"
	if e.NoToken {
		return head + ". Add a token that can read " + e.Base + " in AI settings, then try again"
	}
	return head + ", and its account hasn't been granted access to " + e.Base +
		". Accept " + e.Base + "'s license on Hugging Face with that account, then try again"
}

// Unwrap returns the GatedError or GatedAccessError about the base model.
func (e *BaseGatedError) Unwrap() error { return e.err }

// baseModel is the repo a GGUF repo says it was made from, from its
// "base_model:quantized:<repo>" tag, or "base_model:<repo>".
func baseModel(info *hfhub.ModelInfo) string {
	var plain string
	for _, t := range info.Tags {
		if repo, ok := strings.CutPrefix(t, "base_model:quantized:"); ok && hfhub.ValidRepo(repo) {
			return repo
		}
		if repo, ok := strings.CutPrefix(t, "base_model:"); ok && plain == "" && hfhub.ValidRepo(repo) {
			plain = repo
		}
	}
	return plain
}

func hasGGUF(info *hfhub.ModelInfo) bool {
	for _, f := range info.Files {
		if strings.HasSuffix(strings.ToLower(f.Name), ".gguf") {
			return true
		}
	}
	return false
}

// firstGGUF is the first GGUF file at the top of the repo that Loomplane
// would use, or "".
func firstGGUF(info *hfhub.ModelInfo) string {
	for _, f := range info.Files {
		if hfhub.GGUFName(f.Name) {
			return f.Name
		}
	}
	return ""
}

func revisionOrMain(revision string) string {
	if revision == "" {
		return "main"
	}
	return revision
}

// GatedAccessError is a gated repo the workspace's Hugging Face token
// can't download yet. It says what to do and holds nothing secret.
type GatedAccessError struct{ Repo string }

func (e *GatedAccessError) Error() string {
	return e.Repo + " is gated on Hugging Face, and the account of the workspace's Hugging Face token hasn't been granted access. " +
		"Accept its license on Hugging Face with that account, then try again"
}

func gatedAccessError(repo string) error { return &GatedAccessError{Repo: repo} }
