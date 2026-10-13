package identity

import (
	"context"
	"net/http"
	"strings"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/security"
)

// RevalidateStream checks the original credential without extending an idle
// session. Long-lived transports call it again before delivering updates.
type RevalidateStream func(context.Context, *http.Request) (security.Principal, error)

func (e *Extension) revalidateStream(ctx context.Context, request *http.Request) (security.Principal, error) {
	raw, _ := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	raw = strings.TrimSpace(raw)
	var p security.Principal
	if apikeys.IsKey(raw) {
		var err error
		p, err = e.keys.Authenticate(ctx, raw)
		if err != nil {
			return p, err
		}
	} else {
		if raw == "" {
			if cookie, err := request.Cookie(e.cfg.Identity.SessionCookie); err == nil {
				raw = cookie.Value
			}
		}
		if raw == "" {
			return p, security.ErrUnauthenticated
		}
		sess, err := e.engine.ResolveSessionByToken(ctx, raw)
		if err != nil || sess.AppID.String() != e.cfg.Identity.AppID {
			return p, security.ErrUnauthenticated
		}
		if _, userErr := e.engine.ResolveUser(ctx, sess.UserID.String()); userErr != nil {
			return p, security.ErrUnauthenticated
		}
		p, err = security.Resolve(sess.UserID.String(), sess.OrgID.String(), request.Header.Get(security.HeaderOrganization))
		if err != nil {
			return p, err
		}
	}
	if _, found, err := e.membership(ctx, p.OrgID, p.UserID); err != nil {
		return p, err
	} else if !found {
		return p, security.ErrNotMember
	}
	return p, nil
}
