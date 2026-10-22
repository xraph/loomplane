package catalogproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/security"
)

// What the proxy answers when it serves nothing from the admin.
const (
	msgNotFound   = "There's no admin page here. Open the admin again from Loomplane."
	msgExpired    = "This admin link expired. Open it again from Loomplane."
	msgForbidden  = "You no longer have permission to open this admin."
	msgLive       = "This page's live updates aren't available through Loomplane on this target."
	msgTimeout    = "The admin didn't answer in time."
	msgLookup     = "Loomplane couldn't look up this admin link. Try again."
	msgPermission = "Loomplane couldn't check your permission. Try again."
)

// sandboxPolicy runs every page the proxy serves in an opaque origin: its
// scripts run, but it can't read Loomplane's cookies or storage, call
// Loomplane's API as the signed-in user, or navigate the tab that opened
// it.
const sandboxPolicy = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads"

// methods are what the proxy sends on. OPTIONS it answers itself.
var methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// headerTimeout bounds how long the admin takes to start answering. A
// response that has started may stream for as long as the session lasts.
const headerTimeout = 30 * time.Second

// Handler serves /catalog-admin/{token}/{path}. Mount it outside the
// identity guard: the token in the path is the credential.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	rawPath, rawQuery := requestPath(r)
	rest, ok := strings.CutPrefix(rawPath, pathPrefix)
	if !ok {
		refuse(w, http.StatusNotFound, msgNotFound)
		return
	}
	token, rest, _ := strings.Cut(rest, "/")
	rest = "/" + rest
	if !wellFormed(token) {
		refuse(w, http.StatusNotFound, msgNotFound)
		return
	}
	ctx := r.Context()
	hash := hashToken(token)
	rec, err := s.d.Store.Get(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		s.forget(hash)
		refuse(w, http.StatusNotFound, msgNotFound)
		return
	}
	if err != nil {
		refuse(w, http.StatusInternalServerError, msgLookup)
		return
	}
	now := s.d.Now().UTC()
	end := rec.CreatedAt.Add(MaxTTL)
	if !now.Before(rec.ExpiresAt) || !now.Before(end) {
		s.forget(hash)
		refuse(w, http.StatusGone, msgExpired)
		return
	}
	target, ok := targetOf(rec.Kind)
	if !ok {
		refuse(w, http.StatusNotFound, msgNotFound)
		return
	}
	if status, msg := s.recheck(ctx, rec, now); status != 0 {
		refuse(w, status, msg)
		return
	}
	if now.Sub(rec.LastUsedAt) >= touchEvery {
		// A failed write only shortens the session; this request is
		// still the creator's.
		_ = s.d.Store.Touch(ctx, hash, now, minTime(now.Add(IdleTTL), end)) //nolint:errcheck // see above
	}
	if r.Method == http.MethodOptions {
		preflight(w, r)
		return
	}
	if !slices.Contains(methods, r.Method) {
		// TRACE would echo the admin credential back, and nothing an
		// admin page sends needs anything else.
		w.Header().Set("Allow", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		refuse(w, http.StatusMethodNotAllowed, "Loomplane doesn't send this method to an admin.")
		return
	}
	if rest == "/" {
		sandbox(w.Header())
		w.Header().Set("Location", sessionPrefix(token)+target.Root)
		w.WriteHeader(http.StatusFound)
		return
	}
	upPath, upRaw, ok := upstreamPath(rest, target)
	if !ok {
		refuse(w, http.StatusNotFound, msgNotFound)
		return
	}
	ctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	s.forward(ctx, cancel, w, r, rec, target, token, &url.URL{Path: upPath, RawPath: upRaw, RawQuery: rawQuery})
}

// requestPath is the request's path as the browser sent it, still
// escaped, and its query. The router may have trimmed a trailing slash
// from r.URL, so it's read from the request line.
func requestPath(r *http.Request) (rawPath, rawQuery string) {
	uri := r.RequestURI
	if uri == "" {
		return r.URL.EscapedPath(), r.URL.RawQuery
	}
	if !strings.HasPrefix(uri, "/") {
		// The absolute form, http://host/path.
		if _, after, ok := strings.Cut(uri, "://"); ok {
			if i := strings.IndexByte(after, '/'); i >= 0 {
				uri = after[i:]
			} else {
				uri = "/"
			}
		}
	}
	rawPath, rawQuery, _ = strings.Cut(uri, "?")
	return rawPath, rawQuery
}

// upstreamPath is the path on the component for rest, a session's path
// as the browser sent it, or false when the proxy mustn't reach it. Each
// segment is decoded once; a dot segment, an encoded slash or backslash,
// or a control character refuses the whole path, so nothing can climb out
// of the allowed prefixes. Empty segments fold away; a trailing slash is
// kept. The decoded path must be under an allowed prefix, and then
// StripPrefix is removed. It returns the decoded path and its escaped
// form.
func upstreamPath(rest string, t Target) (decodedPath, escapedPath string, ok bool) {
	var decoded, raw []string
	for seg := range strings.SplitSeq(strings.TrimPrefix(rest, "/"), "/") {
		if seg == "" {
			continue
		}
		lower := strings.ToLower(seg)
		if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
			return "", "", false
		}
		d, err := url.PathUnescape(seg)
		if err != nil || d == "." || d == ".." || strings.ContainsFunc(d, func(c rune) bool { return c < 0x20 || c == 0x7f || c == '\\' }) {
			return "", "", false
		}
		decoded, raw = append(decoded, d), append(raw, seg)
	}
	trailing := strings.HasSuffix(rest, "/")
	join := func(segs []string) string {
		p := "/" + strings.Join(segs, "/")
		if trailing && len(segs) > 0 {
			p += "/"
		}
		return p
	}
	p := join(decoded)
	if !slices.ContainsFunc(t.AllowedPrefixes, func(prefix string) bool { return underPrefix(p, prefix) }) {
		return "", "", false
	}
	if t.StripPrefix != "" {
		strip := strings.Split(strings.TrimPrefix(t.StripPrefix, "/"), "/")
		if len(decoded) < len(strip) || !slices.Equal(decoded[:len(strip)], strip) {
			return "", "", false
		}
		decoded, raw = decoded[len(strip):], raw[len(strip):]
	}
	return join(decoded), join(raw), true
}

// recheck asks again, at most once per RecheckEvery, whether the
// session's creator may still modify the project's infrastructure. A
// session whose creator may not ends here. It returns 0 to go on.
func (s *Service) recheck(ctx context.Context, rec *Record, now time.Time) (status int, msg string) {
	s.mu.Lock()
	last, ok := s.checked[rec.TokenHash]
	s.mu.Unlock()
	if ok && now.Sub(last) < RecheckEvery {
		return 0, ""
	}
	err := s.d.Authz.Require(ctx, security.Principal{OrgID: rec.OrgID, UserID: rec.UserID}, security.InfrastructureModify, rec.ProjectID)
	if err == nil {
		s.mu.Lock()
		s.checked[rec.TokenHash] = now
		s.mu.Unlock()
		return 0, ""
	}
	if status := security.StatusFor(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
		s.forget(rec.TokenHash)
		// Ended for good. A failed delete leaves a session the next
		// recheck refuses again.
		_ = s.d.Store.Delete(ctx, rec.TokenHash) //nolint:errcheck // see above
		return http.StatusForbidden, msgForbidden
	}
	return http.StatusServiceUnavailable, msgPermission
}

func (s *Service) forget(hash string) {
	s.mu.Lock()
	delete(s.checked, hash)
	s.mu.Unlock()
}

// forward sends the request to the component over its private channel,
// with the target's credential and without the browser's, and sandboxes
// what comes back.
func (s *Service) forward(ctx context.Context, cancel context.CancelFunc, w http.ResponseWriter, r *http.Request, rec *Record, t Target, token string, up *url.URL) {
	ref := deployments.ResourceRef{ProjectID: rec.ProjectID, EnvironmentID: rec.EnvironmentID, Resource: rec.Resource}
	var credential http.Header
	if t.Header != nil {
		var err error
		credential, err = t.Header(ctx, func(name string) ([]byte, error) {
			return s.d.Access.InternalSecret(ctx, rec.OrgID, ref, name)
		})
		if err != nil {
			refuse(w, http.StatusBadGateway, "Loomplane couldn't sign in to this admin: "+err.Error())
			return
		}
	}
	ch, err := s.d.Access.InternalChannel(ctx, rec.OrgID, deployments.ComponentRef{ResourceRef: ref, Component: t.Component, Port: t.Port})
	if err != nil {
		refuse(w, http.StatusBadGateway, "Loomplane couldn't reach this admin: "+err.Error())
		return
	}
	defer func() { _ = ch.Close() }() //nolint:errcheck // the response is already out

	upgrade := isUpgrade(r.Header)
	u := *ch.BaseURL
	u.Path, u.RawPath, u.RawQuery = up.Path, up.RawPath, up.RawQuery
	out, err := http.NewRequestWithContext(ctx, r.Method, ch.BaseURL.String(), http.NoBody)
	if err != nil {
		refuse(w, http.StatusBadRequest, "Loomplane can't send this request on.")
		return
	}
	out.URL, out.Host = &u, ""
	out.Header = requestHeader(r.Header, credential, upgrade)
	if r.ContentLength != 0 && r.Body != nil {
		out.Body, out.ContentLength = r.Body, r.ContentLength
	}

	// The channel's own client bounds the whole exchange and refuses
	// redirects. The proxy hands redirects to the browser and streams, so
	// it uses the channel's transport, which still sends only to the
	// component and never with an Authorization header.
	timer := time.AfterFunc(headerTimeout, cancel)
	res, err := ch.Client.Transport.RoundTrip(out)
	stopped := timer.Stop()
	if err == nil && !stopped {
		_ = res.Body.Close() // abandoned
		err = context.DeadlineExceeded
	}
	switch {
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || !stopped):
		refuse(w, http.StatusGatewayTimeout, msgTimeout)
		return
	case err != nil:
		refuse(w, http.StatusBadGateway, "Loomplane couldn't reach this admin: "+err.Error())
		return
	}
	if upgrade {
		s.switchProtocols(ctx, w, res)
		return
	}
	defer func() { _ = res.Body.Close() }() // read-only
	respond(w, r, res, t.AllowedPrefixes, sessionPrefix(token))
}

// switchProtocols joins the browser's connection to the component's once
// the component has switched to WebSocket, until either side closes or
// the session's absolute lifetime ends.
func (s *Service) switchProtocols(ctx context.Context, w http.ResponseWriter, res *http.Response) {
	upstream, ok := res.Body.(io.ReadWriteCloser)
	if res.StatusCode != http.StatusSwitchingProtocols || !ok {
		_ = res.Body.Close() // refused
		refuse(w, http.StatusBadGateway, msgLive)
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = upstream.Close() // refused
		refuse(w, http.StatusBadGateway, msgLive)
		return
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = upstream.Close() }()
	h := responseHeader(res.Header, true)
	if _, err = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return
	}
	if err = h.Write(brw); err != nil {
		return
	}
	if _, err = brw.WriteString("\r\n"); err != nil || brw.Flush() != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, brw); done <- struct{}{} }()  //nolint:errcheck // either side may close
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }() //nolint:errcheck // either side may close
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// respond writes the component's response, sandboxed, with its admin
// paths moved under the session's prefix.
func respond(w http.ResponseWriter, r *http.Request, res *http.Response, prefixes []string, sp string) {
	h := w.Header()
	for k, v := range responseHeader(res.Header, false) {
		h[k] = v
	}
	rewriteHeaders(h, prefixes, sp)
	sandbox(h)
	if !rewritable(res) {
		w.WriteHeader(res.StatusCode)
		copyBody(w, res)
		return
	}
	if r.Method == http.MethodHead {
		h.Del("Content-Length")
		w.WriteHeader(res.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxRewrite+1))
	if err != nil {
		for k := range h {
			delete(h, k)
		}
		refuse(w, http.StatusBadGateway, "Loomplane couldn't read this admin's answer: "+err.Error())
		return
	}
	if len(body) > maxRewrite {
		// Too large to rewrite: it goes as it came, still sandboxed.
		w.WriteHeader(res.StatusCode)
		if _, err = w.Write(body); err == nil {
			copyBody(w, res)
		}
		return
	}
	body = rewriteText(body, prefixes, sp)
	if isHTML(res.Header.Get("Content-Type")) {
		if tag, terr := shimScript(sp, prefixes); terr == nil {
			body = injectShim(body, tag)
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Del("Etag")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(body) //nolint:errcheck // the browser went away
}

// copyBody streams the rest of a response, flushing as it goes for an
// event stream.
func copyBody(w http.ResponseWriter, res *http.Response) {
	if !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		_, _ = io.Copy(w, res.Body) //nolint:errcheck // either side went away
		return
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush() // a writer that can't flush still gets the bytes
		}
		if err != nil {
			return
		}
	}
}

// preflight answers a CORS preflight from the sandboxed page itself. The
// page's origin is opaque ("null"); credentials are never allowed.
func preflight(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	sandbox(h)
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
	if asked := r.Header.Get("Access-Control-Request-Headers"); asked != "" {
		h.Set("Access-Control-Allow-Headers", asked)
	}
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// sandbox sets the headers every proxied answer carries.
func sandbox(h http.Header) {
	h.Set("Content-Security-Policy", sandboxPolicy)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Access-Control-Allow-Origin", "null")
}

// refuse answers with a short plain-text message.
func refuse(w http.ResponseWriter, status int, msg string) {
	h := w.Header()
	sandbox(h)
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n") //nolint:errcheck,gosec // the browser went away; plain text, never sniffed
}

// hopByHop are headers for one connection, never passed on.
var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// browserCredentials are what the browser sends of Loomplane's own
// session, or claims about where the request came from, that the
// component never sees.
var browserCredentials = []string{"Cookie", "Authorization", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"}

// requestHeader is what the component gets: the browser's headers less
// hop-by-hop ones, its credentials, and any header the target's
// credential sets, then the target's credential. Bodies come unencoded so
// they can be rewritten.
func requestHeader(in, credential http.Header, upgrade bool) http.Header {
	h := in.Clone()
	if h == nil {
		h = http.Header{}
	}
	upgradeTo := h.Get("Upgrade")
	dropHopByHop(h)
	for _, k := range browserCredentials {
		h.Del(k)
	}
	for k := range credential {
		h.Del(k)
	}
	h.Set("Accept-Encoding", "identity")
	for k, v := range credential {
		for _, value := range v {
			h.Add(k, value)
		}
	}
	if upgrade {
		h.Set("Connection", "Upgrade")
		h.Set("Upgrade", upgradeTo)
	}
	return h
}

// droppedResponse are the component's headers the browser never sees:
// they'd act on Loomplane's origin, not the admin's.
var droppedResponse = []string{
	"Set-Cookie", "Set-Cookie2", "Content-Security-Policy", "Content-Security-Policy-Report-Only",
	"Strict-Transport-Security", "Clear-Site-Data", "Alt-Svc", "Service-Worker-Allowed",
	"Public-Key-Pins", "Public-Key-Pins-Report-Only", "Report-To", "Reporting-Endpoints", "Nel",
	"Www-Authenticate", "Origin-Agent-Cluster",
}

// responseHeader is what the browser gets of the component's headers.
// switched keeps the headers of a protocol switch.
func responseHeader(in http.Header, switched bool) http.Header {
	h := in.Clone()
	if h == nil {
		h = http.Header{}
	}
	upgradeTo := h.Get("Upgrade")
	dropHopByHop(h)
	for _, k := range droppedResponse {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(k, "Access-Control-") {
			delete(h, k)
		}
	}
	if switched {
		h.Set("Connection", "Upgrade")
		h.Set("Upgrade", upgradeTo)
	}
	return h
}

func dropHopByHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, k := range hopByHop {
		h.Del(k)
	}
}

// isUpgrade reports whether the browser asks to switch protocols, as a
// WebSocket does.
func isUpgrade(h http.Header) bool {
	if h.Get("Upgrade") == "" {
		return false
	}
	for _, v := range h.Values("Connection") {
		for name := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(name), "upgrade") {
				return true
			}
		}
	}
	return false
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
