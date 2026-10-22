package catalogproxy

import (
	"bytes"
	_ "embed" // shim.js
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// maxRewrite is the largest body the proxy rewrites. A larger one passes
// through as it came, still sandboxed: its admin paths aren't moved under
// the session, and a page that large gets no shim.
const maxRewrite = 8 << 20

// shimSource is a function expression, (function (window, config) {...}),
// that keeps a page's own requests and history entries under its session.
//
//go:embed shim.js
var shimSource string

// rewritten are the content types whose bodies have their admin paths
// moved under the session's prefix.
var rewritten = map[string]bool{"text/html": true, "text/css": true, "text/javascript": true, "application/javascript": true}

func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt, _, _ = strings.Cut(contentType, ";")
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

func isHTML(contentType string) bool { return mediaType(contentType) == "text/html" }

// rewritable reports whether a response's body is rewritten: one of the
// rewritten types, whole and unencoded.
func rewritable(res *http.Response) bool {
	switch res.StatusCode {
	case http.StatusPartialContent, http.StatusNoContent, http.StatusNotModified:
		return false
	}
	if !rewritten[mediaType(res.Header.Get("Content-Type"))] {
		return false
	}
	enc := strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Encoding")))
	return enc == "" || enc == "identity"
}

// pathBoundary reports whether what follows a prefix in a URL ends the
// prefix's last segment, so /admin matches /admin/x and "/admin" but not
// /administrator.
func pathBoundary(after []byte) bool {
	return len(after) == 0 || strings.IndexByte("/?#\"'`) \t\r\n", after[0]) >= 0
}

// bases are the allowed prefixes without their trailing slashes.
func bases(prefixes []string) [][]byte {
	out := make([][]byte, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, []byte(strings.TrimSuffix(p, "/")))
	}
	return out
}

// rewriteText moves each "<prefix>, '<prefix>, `<prefix> and (<prefix> of
// an allowed prefix under sp, the session's prefix, in one pass.
func rewriteText(b []byte, prefixes []string, sp string) []byte {
	bs := bases(prefixes)
	var out bytes.Buffer
	last := 0
	for i, c := range b {
		if c != '"' && c != '\'' && c != '`' && c != '(' {
			continue
		}
		rest := b[i+1:]
		for _, base := range bs {
			if !bytes.HasPrefix(rest, base) || !pathBoundary(rest[len(base):]) {
				continue
			}
			if last == 0 {
				out.Grow(len(b) + 64*len(sp))
			}
			out.Write(b[last : i+1])
			out.WriteString(sp)
			last = i + 1
			break
		}
	}
	if last == 0 {
		return b
	}
	out.Write(b[last:])
	return out.Bytes()
}

// rewriteLocation moves a redirect or similar URL under the session when
// it's an admin path, given either as a path or as the component's own
// absolute URL. Any other URL is left alone.
func rewriteLocation(v string, prefixes []string, sp string) string {
	p := v
	if !strings.HasPrefix(v, "/") {
		u, err := url.Parse(v)
		if err != nil || !strings.EqualFold(u.Scheme, "http") || !strings.EqualFold(u.Host, "component") || u.User != nil {
			return v
		}
		p = u.EscapedPath()
		if p == "" {
			p = "/"
		}
		if u.RawQuery != "" || u.ForceQuery {
			p += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			p += "#" + u.EscapedFragment()
		}
	} else if strings.HasPrefix(v, "//") {
		return v
	}
	for _, base := range bases(prefixes) {
		if strings.HasPrefix(p, string(base)) && pathBoundary([]byte(p[len(base):])) {
			return sp + p
		}
	}
	return v
}

// locationHeaders carry a URL the browser follows: redirects, and htmx's
// response headers.
var locationHeaders = []string{"Location", "Content-Location", "Hx-Redirect", "Hx-Location", "Hx-Push-Url", "Hx-Replace-Url"}

// rewriteHeaders moves the admin paths of the headers the browser follows
// under the session. htmx's may hold JSON, so a quoted path is moved too.
func rewriteHeaders(h http.Header, prefixes []string, sp string) {
	for _, k := range locationHeaders {
		values := h[http.CanonicalHeaderKey(k)]
		for i, v := range values {
			v = rewriteLocation(v, prefixes, sp)
			if strings.HasPrefix(k, "Hx-") {
				v = string(rewriteText([]byte(v), prefixes, sp))
			}
			values[i] = v
		}
	}
}

// shimScript is the <script> that installs the shim in a page of the
// session at sp. The config is JSON, which escapes <, > and &, so it
// can't close the script.
func shimScript(sp string, prefixes []string) (string, error) {
	config, err := json.Marshal(struct {
		Base     string   `json:"base"`
		Prefixes []string `json:"prefixes"`
	}{sp, prefixes})
	if err != nil {
		return "", err
	}
	return "<script>" + strings.TrimSpace(shimSource) + "(window, " + string(config) + ");</script>", nil
}

// injectShim puts tag right after the page's <head>, or at the top of its
// <body> when it has no head. A fragment with neither, such as an htmx
// partial, gets none: the page it lands in already has it.
func injectShim(b []byte, tag string) []byte {
	at := afterOpenTag(b, "head")
	if at < 0 {
		at = afterOpenTag(b, "body")
	}
	if at < 0 {
		return b
	}
	out := make([]byte, 0, len(b)+len(tag))
	out = append(out, b[:at]...)
	out = append(out, tag...)
	return append(out, b[at:]...)
}

// afterOpenTag is the offset just past the first <name ...> tag in b,
// case-insensitively, or -1.
func afterOpenTag(b []byte, name string) int {
	open := []byte("<" + name)
	for from := 0; from < len(b); {
		i := indexFold(b[from:], open)
		if i < 0 {
			return -1
		}
		i += from
		next := i + len(open)
		if next < len(b) && strings.IndexByte(">/ \t\r\n\f", b[next]) >= 0 {
			end := bytes.IndexByte(b[next:], '>')
			if end < 0 {
				return -1
			}
			return next + end + 1
		}
		from = i + 1
	}
	return -1
}

// indexFold is bytes.Index, ignoring ASCII case.
func indexFold(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if b[i] == sub[0] && bytes.EqualFold(b[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}
