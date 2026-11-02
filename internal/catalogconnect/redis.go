package catalogconnect

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/recipes"
)

// RedisProber checks a Redis endpoint over RESP: AUTH when the DSN has a
// password, PING, then INFO server for redis_version.
type RedisProber struct{}

var _ Prober = RedisProber{}

// maxBulk bounds a reply the prober reads. INFO server is a few KiB.
const maxBulk = 1 << 20

// Probe implements Prober.
func (RedisProber) Probe(ctx context.Context, dial DialFunc, dsn string, req recipes.Requirement) (*Report, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" {
		return nil, errors.New("the stored DSN can't be read as a redis URL")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, unreachable(err)
	}
	defer func() { _ = conn.Close() }() // a probe only reads
	if deadline, ok := ctx.Deadline(); ok {
		if err = conn.SetDeadline(deadline); err != nil {
			return nil, unreachable(err)
		}
	}
	// Cancelling the probe closes the connection, which ends a read.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if u.Scheme == "rediss" {
		tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err = tc.HandshakeContext(ctx); err != nil {
			return nil, redisFailure(err)
		}
		conn = tc
	}
	c := &respConn{w: conn, r: bufio.NewReader(conn)}
	if password, ok := u.User.Password(); ok && password != "" {
		args := []string{"AUTH", password}
		if name := u.User.Username(); name != "" {
			args = []string{"AUTH", name, password}
		}
		if _, err = c.do(args...); err != nil {
			return nil, redisFailure(err)
		}
	}
	pong, err := c.do("PING")
	if err != nil {
		return nil, redisFailure(err)
	}
	if pong != "PONG" {
		return nil, refused(fmt.Sprintf("PING answered %q, not PONG", pong))
	}
	info, err := c.do("INFO", "server")
	if err != nil {
		return nil, redisFailure(err)
	}
	report := &Report{}
	for line := range strings.SplitSeq(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			report.Version = v
			break
		}
	}
	checkVersion(report, EngineRedis, req)
	return report, nil
}

// serverError is an error reply, such as WRONGPASS. Redis doesn't repeat
// the password in one.
type serverError string

func (e serverError) Error() string { return string(e) }

func redisFailure(err error) error {
	var se serverError
	var ne net.Error
	switch {
	case errors.As(err, &se):
		return refused(string(se))
	case errors.As(err, &ne) && ne.Timeout():
		return errTimedOut
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return unreachable(errors.New("the connection closed before the server answered"))
	}
	return unreachable(err)
}

// respConn sends commands and reads replies, enough of RESP2 for a probe.
type respConn struct {
	w io.Writer
	r *bufio.Reader
}

// do sends a command and returns its reply: a simple string, a bulk
// string or an integer, as text. An error reply is a serverError.
func (c *respConn) do(args ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(c.w, b.String()); err != nil {
		return "", err
	}
	line, err := c.line()
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", errors.New("the server sent an empty reply")
	}
	switch line[0] {
	case '+', ':':
		return line[1:], nil
	case '-':
		return "", serverError(line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n > maxBulk {
			return "", fmt.Errorf("the server sent a bulk reply of length %q", line[1:])
		}
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err = io.ReadFull(c.r, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	}
	return "", fmt.Errorf("the server sent an unexpected %q reply", line[:1])
}

func (c *respConn) line() (string, error) {
	var out []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return "", err
		}
		out = append(out, chunk...)
		if len(out) > maxBulk {
			return "", errors.New("the server sent a reply line that's too long")
		}
		if !isPrefix {
			return string(out), nil
		}
	}
}
