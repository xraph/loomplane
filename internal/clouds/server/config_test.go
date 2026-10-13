package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return signer, raw
}

func TestParseConfig_pinsTheHostAndKeepsCredentialsPrivate(t *testing.T) {
	host, _ := testKey(t)
	_, raw := testKey(t)
	settings := map[string]string{"host": "127.0.0.1", "user": "deploy", "host_key": string(ssh.MarshalAuthorizedKey(host.PublicKey()))}
	cfg, err := ParseConfig(raw, settings)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Details["port"] != "22" || cfg.Details["docker_socket"] != "/var/run/docker.sock" || cfg.Signer == nil {
		t.Fatalf("config: %+v", cfg.Details)
	}
	for _, value := range cfg.Details {
		if strings.Contains(value, "PRIVATE KEY") {
			t.Fatal("credential in metadata")
		}
	}
	for key, value := range map[string]string{"host": "https://host", "port": "0", "user": "deploy\nroot", "host_key": "invalid", "docker_socket": "relative"} {
		t.Run(key, func(t *testing.T) {
			bad := map[string]string{}
			for k, v := range settings {
				bad[k] = v
			}
			bad[key] = value
			if _, err := ParseConfig(raw, bad); err == nil {
				t.Fatal("unsafe setting accepted")
			}
		})
	}
}
