package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Capacity reads actual daemon capacity after the pinned connection check.
func (c *Client) Capacity(ctx context.Context) (cpus int, memoryBytes int64, err error) {
	var info struct {
		NCPU     int
		MemTotal int64
	}
	if checkErr := c.getJSON(ctx, "/info", &info); checkErr != nil {
		return 0, 0, checkErr
	}
	if info.NCPU < 1 || info.MemTotal < 1 {
		return 0, 0, errors.New("docker capacity is unavailable")
	}
	return info.NCPU, info.MemTotal, nil
}

// CheckVacant refuses adoption of a host with existing containers or listeners
// on the ingress ports. It runs a fixed read-only command over pinned SSH.
func (c *Client) CheckVacant(ctx context.Context) error {
	response, err := c.Request(ctx, http.MethodGet, "/containers/json?all=1", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("cannot inspect existing Docker workloads")
	}
	var containers []json.RawMessage
	if checkErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&containers); checkErr != nil {
		return checkErr
	}
	if len(containers) > 0 {
		return errors.New("adoption refused: existing Docker workloads need an explicit migration")
	}
	session, err := c.ssh.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	output, err := session.Output("LC_ALL=C ss -H -ltn '( sport = :80 or sport = :443 or sport = :2019 )'")
	if err != nil {
		return errors.New("cannot verify ingress listener availability")
	}
	if strings.TrimSpace(string(output)) != "" {
		return errors.New("adoption refused: ingress listeners are already in use")
	}
	return nil
}
