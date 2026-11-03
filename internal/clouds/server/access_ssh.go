package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ingressListenersCommand lists listeners on the ingress ports.
const ingressListenersCommand = "LC_ALL=C ss -H -ltn '( sport = :80 or sport = :443 or sport = :2019 )'"

// sshAccess reaches the host through an SSH connection pinned to its host key.
type sshAccess struct {
	client *ssh.Client
	socket string
}

// dialSSH authenticates without reading SSH config, agents or ambient keys.
func dialSSH(ctx context.Context, cfg *Config) (*sshAccess, error) {
	if cfg == nil || cfg.Signer == nil || cfg.HostKey == nil {
		return nil, ErrInvalid
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("reach SSH server: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	algorithms := []string{cfg.HostKey.Type()}
	if cfg.HostKey.Type() == ssh.KeyAlgoRSA {
		algorithms = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	sshConfig := &ssh.ClientConfig{HostKeyAlgorithms: algorithms, User: cfg.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(cfg.Signer)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(key.Marshal(), cfg.HostKey.Marshal()) {
			return ErrRejected
		}
		return nil
	}}
	cc, chans, requests, err := ssh.NewClientConn(conn, cfg.Address, sshConfig)
	stop()
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), ErrRejected.Error()) {
			return nil, ErrRejected
		}
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = cc.Close()
		return nil, err
	}
	return &sshAccess{client: ssh.NewClient(cc, chans, requests), socket: cfg.Socket}, nil
}

func (a *sshAccess) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return a.client.DialContext(ctx, network, address)
}

func (a *sshAccess) DialDocker(ctx context.Context) (net.Conn, error) {
	return a.client.DialContext(ctx, "unix", a.socket)
}

func (a *sshAccess) Close() error { return a.client.Close() }

// FencedRequest runs the fence script in its own session. The request goes
// over stdin and the response comes back on stdout.
func (a *sshAccess) FencedRequest(ctx context.Context, mode string, f EffectFence, daemon, socket, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	session, err := a.client.NewSession()
	if err != nil {
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	script, err := fenceScript()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	// The constant script contains no credentials. Request headers and bodies use
	// stdin only and never enter a shell command, file log or error response.
	if err = session.Start("python3 -c '" + strings.ReplaceAll(script, "'", "'\"'\"'") + "'"); err != nil {
		_ = session.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	request := struct {
		Mode, Daemon, Socket, Method, Path string
		Fence                              EffectFence
		Headers                            http.Header
	}{mode, daemon, socket, method, path, f, headers}
	if err = json.NewEncoder(input).Encode(request); err == nil && body != nil {
		_, err = io.Copy(input, body)
	}
	closeErr := input.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		stop()
		_ = session.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(output), nil)
	if err != nil {
		stop()
		_ = session.Close()
		return nil, errors.New("runtime effect fence outcome is unknown")
	}
	response.Body = &fenceBody{ReadCloser: response.Body, closeSession: session.Close, stop: stop}
	return response, nil
}

func (a *sshAccess) CgroupLimits(ctx context.Context) (cpuMillis, memoryBytes int64, err error) {
	session, err := a.client.NewSession()
	if err != nil {
		return 0, 0, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	out, err := session.Output(effectiveCapacityCommand)
	if err != nil {
		return 0, 0, errors.New("effective Docker cgroup limits are unavailable")
	}
	var limits struct {
		CPU    int64 `json:"cpu"`
		Memory int64 `json:"memory"`
	}
	if checkErr := json.Unmarshal(out, &limits); checkErr != nil {
		return 0, 0, checkErr
	}
	return limits.CPU, limits.Memory, nil
}

func (a *sshAccess) GPUProbe(ctx context.Context) ([]byte, error) {
	session, err := a.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	return session.Output(gpuProbeCommand)
}

func (a *sshAccess) Filesystem(ctx context.Context, root string) (FilesystemCapacity, error) {
	command, err := storageCapacityCommand(root)
	if err != nil {
		return FilesystemCapacity{}, err
	}
	session, err := a.client.NewSession()
	if err != nil {
		return FilesystemCapacity{}, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	raw, err := session.Output(command)
	if err != nil {
		return FilesystemCapacity{}, errors.New("docker data filesystem capacity is unavailable")
	}
	return parseFilesystemCapacity(raw)
}

func (a *sshAccess) IngressListeners(ctx context.Context) ([]int, error) {
	session, err := a.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	output, err := session.Output(ingressListenersCommand)
	if err != nil {
		return nil, errors.New("cannot verify ingress listener availability")
	}
	return parseListeners(output)
}

// parseListeners reads the local ports from ss -H -ltn output. A line it
// can't read fails the whole probe, so nothing in use passes for free.
func parseListeners(output []byte) ([]int, error) {
	ports := []int{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 4 {
			return nil, errors.New("cannot verify ingress listener availability")
		}
		local := fields[3]
		port, err := strconv.Atoi(local[strings.LastIndexByte(local, ':')+1:])
		if err != nil || port < 1 || port > 65535 || !strings.Contains(local, ":") {
			return nil, errors.New("cannot verify ingress listener availability")
		}
		if !slices.Contains(ports, port) {
			ports = append(ports, port)
		}
	}
	slices.Sort(ports)
	return ports, nil
}
