package nodes

import (
	"net/http"
	"strconv"
	"time"

	authmw "github.com/xraph/authsome/middleware"
	"github.com/xraph/forge"
)

// rateWindow is the window every per-address node limit counts over.
const rateWindow = time.Minute

// Routes a per-address limit counts separately.
const (
	limitJoin      = "join"
	limitChallenge = "challenge"
)

// limited counts one request from the caller's address against route's
// budget of perMinute. When the address is over it, or the limiter can't
// answer, it writes the refusal and reports true. The address is the one
// authsome trusts: forwarding headers count only from a trusted proxy, so a
// caller can't claim a fresh address on each request.
func (e *Extension) limited(w http.ResponseWriter, r *http.Request, route string, perMinute int) bool {
	if e.limiter == nil || perMinute <= 0 {
		return false
	}
	ip := authmw.ClientIP(r)
	if ip == "" {
		// Never one bucket for every caller whose address didn't parse.
		ip = r.RemoteAddr
	}
	ok, err := e.limiter.Allow(r.Context(), "loomplane-nodes:"+route+":"+ip, perMinute, rateWindow)
	switch {
	case err != nil:
		// Closed, like authsome's own limits: an outage of the shared store
		// must not become an unlimited window.
		e.Logger().Warn("nodes: the rate limiter could not answer", forge.F("route", route), forge.F("error", err.Error()))
		w.Header().Set("Retry-After", "5")
		writeJSONError(w, http.StatusServiceUnavailable, "rate limiting is temporarily unavailable, try again shortly")
		return true
	case !ok:
		e.Logger().Debug("nodes: an address went over its rate limit", forge.F("route", route), forge.F("remote", ip), forge.F("per_minute", perMinute))
		w.Header().Set("Retry-After", strconv.Itoa(int(rateWindow.Seconds())))
		writeJSONError(w, http.StatusTooManyRequests, "too many requests from this address; try again in a minute")
		return true
	}
	return false
}
