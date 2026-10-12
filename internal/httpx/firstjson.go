// Package httpx holds plain net/http helpers Loomplane wraps around Forge.
package httpx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// FirstJSON trims a response body holding two JSON documents down to the
// first. Some of authsome's organization handlers both call ctx.JSON and
// return the value, and Forge writes the returned value again, so their
// bodies are two documents no JSON client can parse. tap carries the same
// fix (internal/httpx in github.com/xraph/tap). Remove it once authsome
// fixes those handlers.
//
// It buffers the whole response, so only put it in front of authsome's
// small JSON API, never a stream.
func FirstJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bw := &bufferedWriter{header: http.Header{}}
		next.ServeHTTP(bw, r)

		body := bw.buf.Bytes()
		dec := json.NewDecoder(bytes.NewReader(body))
		var first json.RawMessage
		if err := dec.Decode(&first); err == nil && dec.More() {
			body = append(bytes.TrimSpace(first), '\n')
		}
		for k, vs := range bw.header {
			w.Header()[k] = vs
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		status := bw.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(body) //nolint:errcheck // the client went away; nothing to do
	})
}

type bufferedWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header { return b.header }

func (b *bufferedWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
