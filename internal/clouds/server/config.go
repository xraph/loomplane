// Package server connects to Docker on a host, through an explicitly pinned
// SSH server or the streams of a joined node.
package server

import (
	"bytes"
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

	"github.com/xraph/loomplane/internal/buildpolicy"
)

var (
	ErrInvalid  = errors.New("invalid server connection")
	ErrRejected = errors.New("the SSH server refused the credential or host key")
	userPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
)

// Config retains the signer only in memory; Details may be persisted.
// Node is set, and the SSH fields empty, for a joined node.
type Config struct {
	Address, User, Socket string
	HostKey               ssh.PublicKey
	Signer                ssh.Signer
	Node                  *NodeTarget
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

// ParseConfig accepts a PEM key or a JSON private_key/passphrase pair, or
// the node marker NodeCredential makes.
func ParseConfig(raw []byte, settings map[string]string) (*Config, error) {
	if err := buildpolicy.ValidateWorkers(settings["build_workers"]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if settings["build_runtime_class"] != "" {
		return nil, fmt.Errorf("%w: Kata RuntimeClass requires Kubernetes", ErrInvalid)
	}
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, fmt.Errorf("%w: provide an SSH private key", ErrInvalid)
	}
	if IsNodeCredential(raw) {
		return parseNodeConfig(raw, settings)
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
	socket, err := dockerSocket(settings)
	if err != nil {
		return nil, err
	}
	advertise := strings.TrimSpace(settings["advertise_host"])
	if advertise == "" {
		advertise = host
	}
	if checkErr := checkAdvertiseHost(advertise); checkErr != nil {
		return nil, checkErr
	}
	return &Config{Address: net.JoinHostPort(host, strconv.Itoa(port)), User: user, Socket: socket, HostKey: pub, Signer: signer, Details: map[string]string{
		"build_workers": settings["build_workers"], "host": host, "port": strconv.Itoa(port), "user": user, "host_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), "docker_socket": socket, "advertise_host": advertise,
	}}, nil
}

// dockerSocket is the daemon socket settings name, by default Docker's own.
func dockerSocket(settings map[string]string) (string, error) {
	socket := settings["docker_socket"]
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if !path.IsAbs(socket) || path.Clean(socket) != socket || len(socket) > 4096 || strings.ContainsAny(socket, "\x00\r\n") {
		return "", fmt.Errorf("%w: Docker socket must be an absolute Unix path", ErrInvalid)
	}
	return socket, nil
}

func checkAdvertiseHost(advertise string) error {
	if len(advertise) > 253 || strings.ContainsAny(advertise, "/\\@?# \t\r\n") || (strings.Contains(advertise, ":") && net.ParseIP(advertise) == nil) {
		return fmt.Errorf("%w: advertised host must be a hostname or IP address", ErrInvalid)
	}
	return nil
}

// nodeMarker is the JSON of a node connection's credential.
type nodeMarker struct {
	Node *nodeRef `json:"loomplane_node"`
}

type nodeRef struct {
	OrgID  string `json:"org_id"`
	NodeID string `json:"node_id"`
}

// NodeCredential is a node connection's credential. It holds nothing
// secret: Loomplane makes it on read from the connection's node, and the
// node's own streams are what authenticate.
func NodeCredential(orgID, nodeID string) []byte {
	raw, err := json.Marshal(nodeMarker{Node: &nodeRef{OrgID: orgID, NodeID: nodeID}})
	if err != nil {
		return nil
	}
	return raw
}

// IsNodeCredential reports whether raw is a node connection's credential.
func IsNodeCredential(raw []byte) bool {
	var marker nodeMarker
	return json.Unmarshal(raw, &marker) == nil && marker.Node != nil
}

// parseNodeConfig is ParseConfig for the node marker. The node's identity
// generation comes from the reviewed settings, and there is no key to hold.
func parseNodeConfig(raw []byte, settings map[string]string) (*Config, error) {
	var marker nodeMarker
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil || marker.Node == nil {
		return nil, fmt.Errorf("%w: malformed node credential", ErrInvalid)
	}
	orgID, nodeID := marker.Node.OrgID, marker.Node.NodeID
	if orgID == "" || nodeID == "" || len(orgID) > 128 || len(nodeID) > 128 {
		return nil, fmt.Errorf("%w: the node credential names no node", ErrInvalid)
	}
	if recorded := settings["node_id"]; recorded != "" && recorded != nodeID {
		return nil, fmt.Errorf("%w: the node credential names another node", ErrInvalid)
	}
	generation, err := strconv.ParseInt(settings["identity_generation"], 10, 64)
	if err != nil || generation < 1 {
		return nil, fmt.Errorf("%w: provide the node's identity generation", ErrInvalid)
	}
	socket, err := dockerSocket(settings)
	if err != nil {
		return nil, err
	}
	advertise := strings.TrimSpace(settings["advertise_host"])
	if checkErr := checkAdvertiseHost(advertise); checkErr != nil {
		return nil, checkErr
	}
	return &Config{Socket: socket, Node: &NodeTarget{OrgID: orgID, NodeID: nodeID, IdentityGeneration: generation}, Details: map[string]string{
		"node_id": nodeID, "identity_generation": strconv.FormatInt(generation, 10), "docker_socket": socket, "advertise_host": advertise, "build_workers": settings["build_workers"],
	}}, nil
}
