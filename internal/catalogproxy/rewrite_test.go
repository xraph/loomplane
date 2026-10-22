package catalogproxy

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestProxyRewritesAbsoluteAdminPaths(t *testing.T) {
	h := newHarness(t, sampleTarget())
	pages := map[string]struct{ contentType, body string }{
		"/admin/page": {"text/html; charset=utf-8", `<p><a href="/admin/routes">Routes</a> <div hx-get="/admin/api/stats"></div>` +
			` /administrator and "/administrator" and <a href="/static/logo.png"> <a href="//cdn.example/admin/x"></p>`},
		"/admin/static/site.css": {"text/css", `body{background:url(/admin/static/output.css)} .x{background:url("/admin/img.png")}`},
		"/admin/static/app.js":   {"application/javascript", "fetch('/admin/api/health'); const u = `/admin/api/${id}`; const v = '/adminx';"},
		"/admin/static/mod.js":   {"text/javascript", `import x from "/admin/static/x.js"`},
		"/admin/api/data.json":   {"application/json", `{"next":"/admin/api/data.json?page=2"}`},
	}
	h.up.handler = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin":
			w.Header().Set("Location", "/admin/login")
			w.WriteHeader(http.StatusFound)
		case "/admin/out":
			w.Header().Set("Location", "http://component/admin/bye?x=1")
			w.WriteHeader(http.StatusSeeOther)
		case "/admin/away":
			w.Header().Set("Location", "https://docs.example/admin")
			w.WriteHeader(http.StatusFound)
		case "/admin/later":
			w.Header().Set("Refresh", "5; url=/admin/login")
			w.Header().Add("Link", `</admin/static/site.css>; rel=preload; as=style, <https://cdn.example/admin/x.js>; rel=preload, </static/y.js>; rel=preload`)
			w.Header().Set("Hx-Location", `{"path":"/admin/routes","target":"#main"}`)
			w.WriteHeader(http.StatusNoContent)
		default:
			p := pages[r.URL.Path]
			w.Header().Set("Content-Type", p.contentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(p.body)))
			_, _ = io.WriteString(w, p.body)
		}
	}
	base := h.open(t)
	for path, want := range map[string]string{
		"/admin/page": `<p><a href="` + base + `/admin/routes">Routes</a> <div hx-get="` + base + `/admin/api/stats"></div>` +
			` /administrator and "/administrator" and <a href="/static/logo.png"> <a href="//cdn.example/admin/x"></p>`,
		"/admin/static/site.css": `body{background:url(` + base + `/admin/static/output.css)} .x{background:url("` + base + `/admin/img.png")}`,
		"/admin/static/app.js":   "fetch('" + base + "/admin/api/health'); const u = `" + base + "/admin/api/${id}`; const v = '/adminx';",
		"/admin/static/mod.js":   `import x from "` + base + `/admin/static/x.js"`,
		// JSON isn't rewritten.
		"/admin/api/data.json": `{"next":"/admin/api/data.json?page=2"}`,
	} {
		w := h.do(http.MethodGet, base+path, nil)
		got := w.Body.String()
		if path == "/admin/page" {
			got = strings.Replace(got, shimTag(t, base), "", 1)
		}
		if w.Code != http.StatusOK || got != want {
			t.Errorf("%s:\n got %q\nwant %q", path, got, want)
		}
		if cl := w.Header().Get("Content-Length"); cl != "" && cl != strconv.Itoa(w.Body.Len()) {
			t.Errorf("%s: Content-Length %s for %d bytes", path, cl, w.Body.Len())
		}
	}
	w := h.do(http.MethodGet, base+"/admin/later", nil)
	for k, want := range map[string]string{
		"Refresh":     "5; url=" + base + "/admin/login",
		"Link":        `<` + base + `/admin/static/site.css>; rel=preload; as=style, <https://cdn.example/admin/x.js>; rel=preload, </static/y.js>; rel=preload`,
		"Hx-Location": `{"path":"` + base + `/admin/routes","target":"#main"}`,
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	for path, want := range map[string]string{
		"/admin":      base + "/admin/login",
		"/admin/out":  base + "/admin/bye?x=1",
		"/admin/away": "https://docs.example/admin",
	} {
		w := h.do(http.MethodGet, base+path, nil)
		if got := w.Header().Get("Location"); got != want {
			t.Errorf("%s: Location %q, want %q", path, got, want)
		}
	}
}

// shimTag is the script the proxy puts in each page of base's session.
func shimTag(t *testing.T, base string) string {
	t.Helper()
	tag, err := shimScript(base, sampleTarget().AllowedPrefixes)
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

func TestProxyInjectsShimOnce(t *testing.T) {
	h := newHarness(t, sampleTarget())
	pages := map[string]string{
		"/admin/head":     `<!doctype html><html><HEAD lang="en"><title>x</title></head><body><header>h</header><head></head></body></html>`,
		"/admin/nohead":   `<!doctype html><html><body class="x"><p>x</p></body></html>`,
		"/admin/fragment": `<tr><td>a partial for htmx</td></tr>`,
		"/admin/header":   `<header>not a head</header><body>`,
		"/admin/charset":  `<!doctype html><html><head>` + "\n  " + `<META Charset="utf-8"><title>x</title></head></html>`,
		"/admin/equiv":    `<head><meta http-equiv="Content-Type" content="text/html; charset=utf-8"><meta name="x"></head>`,
		"/admin/notfirst": `<head><title>x</title><meta charset="utf-8"></head>`,
		"/admin/comment":  `<!-- <head> --><html data-x="<head>" title='<body>'><script>var s = "<head>"</script><head><title><body></title></head>`,
		"/admin/inbody":   `<html><body><!-- <head> --><p title="<head>">x</p></body>`,
	}
	h.up.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, pages[r.URL.Path])
	}
	base := h.open(t)
	tag := shimTag(t, base)
	if !strings.HasPrefix(tag, "<script>") || !strings.Contains(tag, `"`+base+`"`) || strings.Count(tag, "<script") != 1 {
		t.Fatalf("shim tag %q", tag)
	}
	for path, want := range map[string]string{
		"/admin/head":     `<!doctype html><html><HEAD lang="en">` + tag + `<title>x</title></head><body><header>h</header><head></head></body></html>`,
		"/admin/nohead":   `<!doctype html><html><body class="x">` + tag + `<p>x</p></body></html>`,
		"/admin/fragment": `<tr><td>a partial for htmx</td></tr>`,
		"/admin/header":   `<header>not a head</header><body>` + tag,
		"/admin/charset":  `<!doctype html><html><head>` + "\n  " + `<META Charset="utf-8">` + tag + `<title>x</title></head></html>`,
		"/admin/equiv":    `<head><meta http-equiv="Content-Type" content="text/html; charset=utf-8">` + tag + `<meta name="x"></head>`,
		"/admin/notfirst": `<head>` + tag + `<title>x</title><meta charset="utf-8"></head>`,
		"/admin/comment":  `<!-- <head> --><html data-x="<head>" title='<body>'><script>var s = "<head>"</script><head>` + tag + `<title><body></title></head>`,
		"/admin/inbody":   `<html><body>` + tag + `<!-- <head> --><p title="<head>">x</p></body>`,
	} {
		w := h.do(http.MethodGet, base+path, nil)
		if got := w.Body.String(); got != want {
			t.Errorf("%s:\n got %q\nwant %q", path, got, want)
		}
	}
}

func TestProxyPassesLargeBodiesThroughUnrewritten(t *testing.T) {
	h := newHarness(t, sampleTarget())
	big := `<head><a href="/admin/x">` + strings.Repeat("a", maxRewrite) + `</a>`
	h.up.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, big)
	}
	base := h.open(t)
	w := h.do(http.MethodGet, base+"/admin/big", nil)
	if w.Code != http.StatusOK || w.Body.String() != big {
		t.Fatalf("got %d, %d bytes", w.Code, w.Body.Len())
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("a large body wasn't sandboxed")
	}
}
