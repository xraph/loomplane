package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ExecOutput streams stdout without mixing diagnostics into a database dump.
// Callers supply fixed native-tool argument lists, never shell commands.
func ExecOutput(ctx context.Context, c *Client, container string, args []string, dst io.Writer) error {
	return execOutput(ctx, c, container, args, dst, false)
}

func execIngressOutput(ctx context.Context, c *Client, container string, args []string, dst io.Writer) error {
	return execOutput(ctx, c, container, args, dst, true)
}

func execOutput(ctx context.Context, c *Client, container string, args []string, dst io.Writer, ingress bool) error {
	var created struct {
		ID string `json:"Id"`
	}
	if err := dockerJSON(ctx, c, http.MethodPost, "/containers/"+url.PathEscape(container)+"/exec", map[string]any{"Cmd": args, "AttachStdout": true, "AttachStderr": true, "Tty": false}, &created); err != nil {
		return err
	}
	if created.ID == "" {
		return errors.New("docker returned no exec identity")
	}
	var response *http.Response
	var err error
	if ingress {
		f, ok := EffectFenceFrom(ctx)
		if !ok {
			return errors.New("ingress exec requires an effect fence")
		}
		response, err = c.fencedRequest(ctx, "ingress-exec", f, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", strings.NewReader(`{"Detach":false,"Tty":false}`), http.Header{"Content-Type": []string{"application/json"}})
	} else {
		response, err = c.Request(ctx, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", strings.NewReader(`{"Detach":false,"Tty":false}`))
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("docker exec returned HTTP %d", response.StatusCode)
	}
	if err := copyDockerOutput(response.Body, dst); err != nil {
		return err
	}
	var result struct {
		Running  bool
		ExitCode int
	}
	if err := dockerJSON(ctx, c, http.MethodGet, "/exec/"+url.PathEscape(created.ID)+"/json", nil, &result); err != nil {
		return err
	}
	if result.Running || result.ExitCode != 0 {
		return fmt.Errorf("database tool did not complete successfully (exit %d)", result.ExitCode)
	}
	return nil
}

func copyDockerOutput(source io.Reader, dst io.Writer) error {
	var header [8]byte
	for {
		_, err := io.ReadFull(source, header[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return errors.New("invalid Docker output frame")
		}
		target := dst
		if header[0] == 2 {
			target = io.Discard
		}
		if _, err = io.CopyN(target, source, int64(binary.BigEndian.Uint32(header[4:]))); err != nil {
			return err
		}
	}
}
