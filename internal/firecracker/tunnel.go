package firecracker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"time"
)

func (h *Host) tunnel(w http.ResponseWriter, r *http.Request, q Request) {
	record, err := h.Inspect(r.Context(), q.Scope)
	if err != nil || record.Current == nil || record.State != "ready" || record.RuntimeIdentity != q.RuntimeIdentity || record.Generation != q.Authority.Generation {
		apiError(w, ErrConflict)
		return
	}
	driver, ok := h.Driver.(TunnelDriver)
	if !ok {
		apiError(w, ErrUnavailable)
		return
	}
	target, err := driver.DialVM(r.Context(), *record.Current)
	if err != nil {
		apiError(w, err)
		return
	}
	defer target.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		apiError(w, ErrUnavailable)
		return
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if _, err = buffer.WriteString("HTTP/1.1 200 OK\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	relay(&tunnelConn{Conn: conn, reader: buffer.Reader}, target)
}

type tunnelConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *tunnelConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func (c *Client) DialService(ctx context.Context, q Request) (net.Conn, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	transport, ok := c.HTTP.Transport.(*http.Transport)
	if !ok {
		return nil, ErrInvalid
	}
	config := transport.TLSClientConfig.Clone()
	config.ServerName = u.Hostname()
	config.NextProtos = []string{"http/1.1"}
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	raw, err := json.Marshal(q)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/v1/tunnel", bytes.NewReader(raw))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err = request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &tunnelConn{Conn: conn, reader: reader}, nil
}
