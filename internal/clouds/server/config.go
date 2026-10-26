// Package server connects to Docker through an explicitly pinned SSH server.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"path"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
)

var (
	ErrInvalid  = errors.New("invalid server connection")
	ErrRejected = errors.New("the SSH server refused the credential or host key")
	userPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
)

// Config retains the signer only in memory; Details may be persisted.
type Config struct {
	Address, User, Socket string
	HostKey               ssh.PublicKey
	Signer                ssh.Signer
	Details               map[string]string
}

// RecordedDetails applies transport defaults while retaining the recorded
// daemon identity. Observed identity must still be compared against these facts.
func (c *Config) RecordedDetails(recorded map[string]string) map[string]string {
	details := maps.Clone(c.Details)
	for _, key := range []string{"docker_id", "architecture"} {
		if value := recorded[key]; value != "" {
			details[key] = value
		}
	}
	return details
}

// ParseConfig accepts a PEM key or a JSON private_key/passphrase pair.
func ParseConfig(raw []byte, settings map[string]string) (*Config, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, fmt.Errorf("%w: provide an SSH private key", ErrInvalid)
	}
	key := struct {
		PrivateKey string `json:"private_key"`
		Passphrase string `json:"passphrase"`
	}{}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		if err := json.Unmarshal(raw, &key); err != nil {
			return nil, fmt.Errorf("%w: malformed key JSON", ErrInvalid)
		}
	} else {
		key.PrivateKey = string(raw)
	}
	var signer ssh.Signer
	var err error
	if key.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key.PrivateKey), []byte(key.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(key.PrivateKey))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: provide a valid private key and its passphrase when encrypted", ErrInvalid)
	}
	host, user := strings.TrimSpace(settings["host"]), strings.TrimSpace(settings["user"])
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/\\@?# \t\r\n") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return nil, fmt.Errorf("%w: provide a hostname or IP address", ErrInvalid)
	}
	if !userPattern.MatchString(user) {
		return nil, fmt.Errorf("%w: provide an SSH username", ErrInvalid)
	}
	port := 22
	if settings["port"] != "" {
		port, err = strconv.Atoi(settings["port"])
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: SSH port must be between 1 and 65535", ErrInvalid)
		}
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(settings["host_key"]))
	if err != nil || strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("%w: provide the server's SSH public host key", ErrInvalid)
	}
	socket := settings["docker_socket"]
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if !path.IsAbs(socket) || path.Clean(socket) != socket || len(socket) > 4096 || strings.ContainsAny(socket, "\x00\r\n") {
		return nil, fmt.Errorf("%w: Docker socket must be an absolute Unix path", ErrInvalid)
	}
	advertise := strings.TrimSpace(settings["advertise_host"])
	if advertise == "" {
		advertise = host
	}
	if len(advertise) > 253 || strings.ContainsAny(advertise, "/\\@?# \t\r\n") || (strings.Contains(advertise, ":") && net.ParseIP(advertise) == nil) {
		return nil, fmt.Errorf("%w: advertised host must be a hostname or IP address", ErrInvalid)
	}
	return &Config{Address: net.JoinHostPort(host, strconv.Itoa(port)), User: user, Socket: socket, HostKey: pub, Signer: signer, Details: map[string]string{
		"host": host, "port": strconv.Itoa(port), "user": user, "host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), "docker_socket": socket, "advertise_host": advertise,
	}}, nil
}
