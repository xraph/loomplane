package ops

import (
	"context"
	"fmt"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/setuptoken"
)

// SetupCompleted reports whether first-run setup has been completed in the
// database at dsn.
func SetupCompleted(ctx context.Context, dsn string) (bool, error) {
	pg := pgdriver.New()
	if err := pg.Open(ctx, dsn); err != nil {
		return false, fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = pg.Close() }()
	return instance.New(pg).SetupCompleted(ctx)
}

// SetupTokenOutput is what `loomplane setup-token` prints. A completed
// instance never derives a token.
func SetupTokenOutput(ctx context.Context, s Settings, raw bool) (string, error) {
	done, err := SetupCompleted(ctx, s.DatabaseURL)
	if err != nil {
		return "", err
	}
	if done {
		return setupTokenOutput(s.PublicURL, "", true, raw), nil
	}
	token, err := setuptoken.Derive(s.SecretKey)
	if err != nil {
		return "", err
	}
	return setupTokenOutput(s.PublicURL, token, false, raw), nil
}

func setupTokenOutput(publicURL, token string, done, raw bool) string {
	switch {
	case done:
		return "Setup is complete."
	case raw:
		return token
	default:
		return "Finish setup at " + setuptoken.Link(publicURL, token)
	}
}
