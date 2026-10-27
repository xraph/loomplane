package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// probeLine bounds both a native readiness reply and the time a silent SSH
// channel can hold the deployment open. SSH channels do not have deadlines.
func probeLine(ctx context.Context, timeout time.Duration, dial func(context.Context) (net.Conn, error), command string, accepts func(string) bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err = io.WriteString(conn, command+"\r\n"); err != nil {
		return fmt.Errorf("send %s: %w", command, err)
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, 512)).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read the %s reply: %w", command, err)
	}
	reply = strings.TrimRight(reply, "\r\n")
	if !accepts(reply) {
		return fmt.Errorf("the port answered %s with %.64q", command, reply)
	}
	return nil
}

func pingMemcached(ctx context.Context, timeout time.Duration, dial func(context.Context) (net.Conn, error)) error {
	return probeLine(ctx, timeout, dial, "version", func(reply string) bool {
		version, ok := strings.CutPrefix(reply, "VERSION ")
		if !ok || version == "" || len(version) > 32 {
			return false
		}
		for _, ch := range version {
			if ch != '.' && ch != '-' && (ch < '0' || ch > '9') {
				return false
			}
		}
		return version[0] >= '0' && version[0] <= '9'
	})
}
