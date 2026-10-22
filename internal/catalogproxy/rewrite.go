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
	refresh := h["Refresh"]
	for i, v := range refresh {
		refresh[i] = rewriteRefresh(v, prefixes, sp)
	}
	links := h["Link"]
	for i, v := range links {
		links[i] = rewriteLink(v, prefixes, sp)
	}
}

// rewriteRefresh moves the URL of a Refresh header, "5; url=/admin/x",
// under the session like a Location.
func rewriteRefresh(v string, prefixes []string, sp string) string {
	i := indexFold([]byte(v), []byte("url="))
	if i < 0 {
		return v
	}
	start := i + len("url=")
	if start < len(v) && (v[start] == '"' || v[start] == '\'') {
		start++
	}
	return v[:start] + rewriteLocation(v[start:], prefixes, sp)
}

// rewriteLink moves each <URL> of a Link header under the session like a
// Location.
func rewriteLink(v string, prefixes []string, sp string) string {
	var out strings.Builder
	for {
		open := strings.IndexByte(v, '<')
		if open < 0 {
			break
		}
		closing := strings.IndexByte(v[open:], '>')
		if closing < 0 {
			break
		}
		closing += open
		out.WriteString(v[:open+1])
		out.WriteString(rewriteLocation(v[open+1:closing], prefixes, sp))
		out.WriteByte('>')
		v = v[closing+1:]
	}
	out.WriteString(v)
	return out.String()
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
// <body> when it has no head. A <meta> naming the charset in the head,
// within the first 1024 bytes, stays ahead of the shim, so the browser's
// encoding prescan of those bytes still finds it. Tags are found the way a
// browser would, near enough: not inside comments, other tags' attributes,
// or script, style, textarea and title text. A fragment with neither head
// nor body, such as an htmx partial, gets no shim: the page it lands in
// already has it.
func injectShim(b []byte, tag string) []byte {
	name, _, at := firstTag(b, 0, "head", "body")
	if at < 0 {
		return b
	}
	if name == "head" {
		prescan := b[:min(len(b), charsetPrescan)]
		for from := at; from < len(prescan); {
			found, start, end := firstTag(prescan, from, "meta", "body")
			if found != "meta" {
				break
			}
			if indexFold(prescan[start:end], []byte("charset")) >= 0 {
				at = end
				break
			}
			from = end
		}
	}
	out := make([]byte, 0, len(b)+len(tag))
	out = append(out, b[:at]...)
	out = append(out, tag...)
	return append(out, b[at:]...)
}

// charsetPrescan is how far into a page a browser looks for its charset.
const charsetPrescan = 1024

// rawText are elements whose content is text, not markup.
var rawText = map[string]bool{"script": true, "style": true, "textarea": true, "title": true}

// firstTag finds, from offset from, the first start tag named one of
// names, and returns its name, its offset and the offset just past its
// ">"; or "", -1 and -1.
func firstTag(b []byte, from int, names ...string) (name string, start, end int) {
	for i := from; i < len(b); {
		lt := bytes.IndexByte(b[i:], '<')
		if lt < 0 {
			return "", -1, -1
		}
		i += lt
		rest := b[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[4:], []byte("-->"))
			if end < 0 {
				return "", -1, -1
			}
			i += 4 + end + 3
			continue
		case len(rest) > 1 && (rest[1] == '!' || rest[1] == '?' || rest[1] == '/'):
			// A doctype, processing instruction or end tag.
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				return "", -1, -1
			}
			i += end + 1
			continue
		case len(rest) < 2 || !isASCIILetter(rest[1]):
			i++
			continue
		}
		j := i + 1
		for j < len(b) && !isHTMLSpace(b[j]) && b[j] != '>' && b[j] != '/' {
			j++
		}
		name := strings.ToLower(string(b[i+1 : j]))
		end := tagEnd(b, j)
		if end < 0 {
			return "", -1, -1
		}
		for _, n := range names {
			if name == n {
				return name, i, end
			}
		}
		i = end
		if rawText[name] {
			closing := indexFold(b[end:], []byte("</"+name))
			if closing < 0 {
				return "", -1, -1
			}
			i = end + closing
		}
	}
	return "", -1, -1
}

// tagEnd is the offset just past the ">" that ends a tag whose attributes
// start at j, skipping quoted attribute values; or -1.
func tagEnd(b []byte, j int) int {
	var quote byte
	for k := j; k < len(b); k++ {
		c := b[k]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return k + 1
		}
	}
	return -1
}

func isHTMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// indexFold is bytes.Index, ignoring ASCII case.
func indexFold(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if lowerASCII(b[i]) == lowerASCII(sub[0]) && bytes.EqualFold(b[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}
