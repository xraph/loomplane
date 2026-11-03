package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
// on the ingress ports. It reads the listeners through the host access, which
// over SSH is a fixed read-only command.
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
	ports, err := c.access.IngressListeners(ctx)
	if err != nil {
		return err
	}
	if len(ports) > 0 {
		return errors.New("adoption refused: ingress listeners are already in use")
	}
	return nil
}
