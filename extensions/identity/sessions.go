package identity

import (
	"context"
	"net/http"
	"time"

	authsome "github.com/xraph/authsome"
	authid "github.com/xraph/authsome/id"
	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/authsome/session"
	"github.com/xraph/authsome/settings"
	"github.com/xraph/forge"
)

const (
	defaultRefreshThreshold  = 5 * time.Minute
	defaultInactivityTimeout = 30 * time.Minute
)

// sessionMiddleware is authsome's session chain as its own Forge extension
// builds it (authsome/extension, Middlewares): authenticate, rotate a token
// that's near expiry, then slide the session's expiry on activity. Without
// the last two a dashboard login dies at the access token's lifetime (an
// hour) however busy the user is.
//
// The order is load-bearing. Sliding the session rewrites ExpiresAt, the
// field auto-refresh reads to decide a token is near expiry, so sliding
// first would keep every session out of the refresh window for good.
func (e *Extension) sessionMiddleware() []forge.Middleware {
	return []forge.Middleware{
		e.authMiddleware(),
		e.autoRefreshMiddleware(),
		e.sessionActivityMiddleware(),
	}
}

// authMiddleware is authsome's own authentication middleware: the session
// cookie bridged to a bearer token, sign-in strategies and DPoP binding.
// authsome only builds it when its engine starts, after routes are
// registered, so it's looked up per request rather than captured here.
func (e *Extension) authMiddleware() forge.Middleware {
	return func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			auth := e.engine.AuthMiddleware()
			if auth == nil {
				return forge.NewHTTPError(http.StatusServiceUnavailable, "sign-in is still starting; try again")
			}
			return auth(next)(c)
		}
	}
}

func (e *Extension) autoRefreshMiddleware() forge.Middleware {
	refresh := func(ctx context.Context, req authmw.RefreshRequest) (*session.Session, error) {
		opts := authsome.RefreshOpts{
			IPAddress: req.IPAddress, UserAgent: req.UserAgent, DPoPProof: req.DPoPProof,
			Method: req.Method, RequestURL: req.RequestURL, KeepRefreshToken: req.KeepRefreshToken,
		}
		// A session the auth middleware loaded from the store carries no
		// refresh plaintext, only the access token it authenticated with.
		if req.RefreshToken == "" {
			return e.engine.RefreshBySessionToken(ctx, req.SessionToken, opts)
		}
		return e.engine.Refresh(ctx, req.RefreshToken, opts)
	}
	config := func(ctx context.Context) authmw.AutoRefreshConfig {
		mgr, opts := e.engine.Settings(), resolveOpts(ctx)
		if mgr == nil {
			return authmw.AutoRefreshConfig{}
		}
		if on, err := settings.Get(ctx, mgr, authsome.SettingAutoRefreshEnabled, opts); err != nil || !on {
			return authmw.AutoRefreshConfig{}
		}
		threshold := defaultRefreshThreshold
		if sec, err := settings.Get(ctx, mgr, authsome.SettingAutoRefreshThresholdSeconds, opts); err == nil && sec > 0 {
			threshold = time.Duration(sec) * time.Second
		}
		// The refresh token never goes out in a header: the dashboard
		// doesn't need it, and a header is easier to leak than a cookie.
		return authmw.AutoRefreshConfig{Enabled: true, Threshold: threshold}
	}
	return authmw.AutoRefreshMiddleware(refresh, config, e.Logger(), e.cookieSetter())
}

func (e *Extension) sessionActivityMiddleware() forge.Middleware {
	config := func(ctx context.Context) authmw.SessionActivityConfig {
		mgr, opts := e.engine.Settings(), resolveOpts(ctx)
		if mgr == nil {
			return authmw.SessionActivityConfig{}
		}
		if on, err := settings.Get(ctx, mgr, authsome.SettingExtendOnActivity, opts); err != nil || !on {
			return authmw.SessionActivityConfig{}
		}
		timeout := defaultInactivityTimeout
		if sec, err := settings.Get(ctx, mgr, authsome.SettingInactivityTimeoutSeconds, opts); err == nil && sec > 0 {
			timeout = time.Duration(sec) * time.Second
		}
		var appID authid.AppID
		if resolved, ok := authmw.AppIDFrom(ctx); ok {
			appID = resolved
		}
		return authmw.SessionActivityConfig{
			Enabled:           true,
			InactivityTimeout: timeout,
			AbsoluteLifetime:  e.engine.AbsoluteLifetimeFor(ctx, appID),
		}
	}
	return authmw.SessionActivityMiddleware(e.engine.Store().TouchSession, config, e.Logger(), e.cookieSetter())
}

// cookieSetter re-sets the session cookie from authsome's own cookie
// template, so its name and attributes (HttpOnly, SameSite, Secure) match
// the cookie sign-in set.
func (e *Extension) cookieSetter() authmw.CookieSetter {
	return func(c forge.Context, token string, maxAge int) {
		mgr := e.engine.Settings()
		if mgr == nil {
			return
		}
		ctx := c.Context()
		appID := ""
		if id, ok := authmw.AppIDFrom(ctx); ok {
			appID = id.String()
		}
		r := c.Request()
		https := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		// authsome's template sets HttpOnly, SameSite and (over https) Secure.
		cookie := authsome.SessionCookieTemplate(ctx, mgr, appID, https) //nolint:gosec // G124: attributes come from authsome's own template
		cookie.Value, cookie.MaxAge = token, maxAge
		http.SetCookie(c.Response(), cookie)
	}
}

func resolveOpts(ctx context.Context) settings.ResolveOpts {
	opts := settings.ResolveOpts{}
	if appID, ok := authmw.AppIDFrom(ctx); ok {
		opts.AppID = appID.String()
	}
	return opts
}
