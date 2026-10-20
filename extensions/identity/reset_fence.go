package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"
)

// Authsome's pinned reset implementation reads the token before consuming it.
// Serialize redemption across replicas while Authsome owns every account write.
func (e *Extension) resetFence() forge.Middleware {
	return func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			r := c.Request()
			if r.Method != http.MethodPost || !strings.EqualFold(path.Clean(r.URL.Path), authsomeAPIPath+"/reset-password") {
				return next(c)
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
			if err != nil || len(body) > 64<<10 {
				return forge.NewHTTPError(http.StatusBadRequest, "invalid password reset request")
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var input struct {
				Token string `json:"token"`
			}
			if json.Unmarshal(body, &input) != nil || input.Token == "" {
				return next(c)
			}
			if !e.resetBusy.CompareAndSwap(false, true) {
				return forge.NewHTTPError(http.StatusTooManyRequests, "try resetting your password again shortly")
			}
			defer e.resetBusy.Store(false)
			// A dedicated connection avoids holding the shared application pool
			// while Authsome queries it. Each process admits one redemption.
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			conn, err := pgx.Connect(ctx, e.cfg.Database.URL)
			if err != nil {
				return forge.NewHTTPError(http.StatusServiceUnavailable, "password reset is temporarily unavailable")
			}
			defer func() {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer closeCancel()
				_ = conn.Close(closeCtx)
			}()
			sum := sha256.Sum256([]byte(e.cfg.Identity.AppID + ":reset:" + input.Token))
			var acquired bool
			lockID := int64(binary.BigEndian.Uint64(sum[:8]) & math.MaxInt64)
			if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired); err != nil {
				return forge.NewHTTPError(http.StatusServiceUnavailable, "password reset is temporarily unavailable")
			}
			if !acquired {
				return forge.NewHTTPError(http.StatusTooManyRequests, "try resetting your password again shortly")
			}
			c.WithContext(ctx)
			return next(c)
		}
	}
}
