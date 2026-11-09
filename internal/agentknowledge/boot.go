// Package agentknowledge turns the agent's knowledge on at boot, or says
// why it can't.
//
// Knowledge keeps its vectors in pgvector, in Loomplane's own Postgres.
// pgvector is never a requirement: weave's pgvector migration group runs
// only when AI and knowledge are both configured, and only after checking
// that the vector extension can be created. When anything stands in the
// way (no embedding model, no price for it, no pgvector, a table made for
// another size), knowledge stays off with that reason, and nothing else
// fails.
package agentknowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/weave"
	"github.com/xraph/weave/vectorstore/pgvector"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

// ReasonNotConfigured is why knowledge is off on a server with no
// embedding model.
const ReasonNotConfigured = "No embedding model is set on this server. Set ai.knowledge in config.yaml to turn knowledge on."

// The other reasons. %v (or %s, %d) is the cause.
const (
	reasonNoPrice    = "ai.knowledge.embedding_model (%s) has no price, so every embedding would be refused. Use an OpenAI model nexus lists, or add it to ai.prices with provider openai-compatible."
	reasonProbe      = "Loomplane couldn't check its database for pgvector: %v"
	reasonNoPgvector = "Knowledge needs the pgvector extension in Loomplane's Postgres: %v"
	reasonDimensions = "The knowledge table was made for a different embedding size: its vectors have %d dimensions, and ai.knowledge.dimensions is %d. Put back the embedding model you had and set ai.knowledge.dimensions to %d, then restart. Moving to the new size takes a manual reset, which the Knowledge docs walk you through."
	// reasonDimensionsUnread is reasonDimensions when the table's size
	// couldn't be read back.
	reasonDimensionsUnread = "The knowledge table was made for a different embedding size than ai.knowledge.dimensions (%d). Put back the embedding model and ai.knowledge.dimensions you had, then restart. Moving to the new size takes a manual reset, which the Knowledge docs walk you through."
	reasonMigrateFail      = "Setting up the knowledge table failed: %v"
)

// probeTimeout bounds the pgvector probe, so a lock held on the extension
// catalog can't hold the server's start. Migrate has no such bound: a
// weave migration that builds an index over a large table must be able to
// finish.
const probeTimeout = time.Minute

// Vectors is where knowledge keeps its vectors.
type Vectors interface {
	// Available reports whether the vector store can be used. reason says
	// why not; err is a failure to ask, never "no".
	Available(ctx context.Context) (ok bool, reason string, err error)
	// Migrate makes the vector table for dimensions, or checks the one
	// there was made for them (weave.ErrDimensionMismatch if not, as a
	// *SizeMismatchError when the table's own size is known).
	Migrate(ctx context.Context, dimensions int) error
}

// SizeMismatchError is Migrate's error when the vector table was made for
// another size. It wraps weave.ErrDimensionMismatch.
type SizeMismatchError struct {
	// Table is the size the table was made for.
	Table int
	err   error
}

func (e *SizeMismatchError) Error() string { return e.err.Error() }

func (e *SizeMismatchError) Unwrap() error { return e.err }

// PgVectors is pgvector in Loomplane's database: weave's pgvector.Available
// and pgvector.Migrate. It is the one place that names pgvector.
func PgVectors(db *grove.DB) Vectors { return pgVectors{db: db} }

type pgVectors struct{ db *grove.DB }

func (p pgVectors) Available(ctx context.Context) (ok bool, reason string, err error) {
	return pgvector.Available(ctx, p.db)
}

func (p pgVectors) Migrate(ctx context.Context, dimensions int) error {
	err := pgvector.Migrate(ctx, p.db, dimensions)
	if !errors.Is(err, weave.ErrDimensionMismatch) {
		return err
	}
	if size, ok := tableSize(ctx, p.db); ok {
		return &SizeMismatchError{Table: size, err: err}
	}
	return err
}

// tableSize reads the size weave_vectors' embedding column was made for.
func tableSize(ctx context.Context, db *grove.DB) (int, bool) {
	rows, err := pgdriver.Unwrap(db).Query(ctx, `SELECT atttypmod FROM pg_attribute
		WHERE attrelid = to_regclass('weave_vectors') AND attname = 'embedding' AND NOT attisdropped`)
	if err != nil {
		return 0, false
	}
	defer func() { _ = rows.Close() }()
	var size int
	if !rows.Next() || rows.Scan(&size) != nil || size < 1 {
		return 0, false
	}
	return size, true
}

// Priced reports whether the server's embedding model has a nexus price.
type Priced func(ctx context.Context) bool

// Boot turns knowledge on if this server can have it, and otherwise says
// why not. Nothing it meets is an error to its caller: the agent starts
// either way.
//
// The order matters. With AI off (Start never calls it then), without an
// embedding model, or without a price for it, Boot never touches the
// database. Only then does it ask whether pgvector can be used, and only
// when it can does it run weave's pgvector group at
// ai.knowledge.dimensions.
func Boot(ctx context.Context, cfg config.AIConfig, priced Priced, v Vectors) agentsettings.KnowledgeStatus {
	k := cfg.Knowledge
	if !cfg.Enabled || !k.Configured() {
		return agentsettings.KnowledgeStatus{Configured: false, Reason: ReasonNotConfigured}
	}
	off := func(reason string) agentsettings.KnowledgeStatus {
		return agentsettings.KnowledgeStatus{Configured: true, Available: false, Reason: reason}
	}
	if priced == nil || !priced(ctx) {
		return off(fmt.Sprintf(reasonNoPrice, k.EmbeddingModel))
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	ok, reason, err := v.Available(probeCtx)
	cancel()
	switch {
	case err != nil:
		return off(fmt.Sprintf(reasonProbe, err))
	case !ok:
		return off(fmt.Sprintf(reasonNoPgvector, reason))
	}
	if err = v.Migrate(ctx, k.Dimensions); err != nil {
		var size *SizeMismatchError
		switch {
		case errors.As(err, &size):
			return off(fmt.Sprintf(reasonDimensions, size.Table, k.Dimensions, size.Table))
		case errors.Is(err, weave.ErrDimensionMismatch):
			return off(fmt.Sprintf(reasonDimensionsUnread, k.Dimensions))
		}
		return off(fmt.Sprintf(reasonMigrateFail, err))
	}
	return agentsettings.KnowledgeStatus{Configured: true, Available: true}
}
