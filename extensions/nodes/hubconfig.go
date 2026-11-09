package nodes

import (
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/nodes"
)

// HKDF info strings. Every replica of one Loomplane derives the same secrets
// from the shared secret_key, so a challenge issued by one replica checks out
// on another, and replicas authenticate each other's hops.
const (
	hopInfo       = "loomplane-node-hop"
	challengeInfo = "loomplane-node-challenge"
	derivedBytes  = 32
)

// derive is HKDF-SHA256 of the instance secret under info.
func derive(secretKey, info string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(secretKey)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("secret_key is not base64")
	}
	return hkdf.Key(sha256.New, raw, nil, info, derivedBytes)
}

// hubConfig is what the hub needs from the settings. replica names this
// process in logs and in the sessions it holds. The pool of CA roots comes
// from nodes.hop_ca_file; a file that is unreadable or holds no certificate
// is an error, never a silent fall back to the system roots.
func hubConfig(cfg config.Config, replica string) (nodes.HubConfig, error) {
	hop, err := derive(cfg.SecretKey, hopInfo)
	if err != nil {
		return nodes.HubConfig{}, fmt.Errorf("derive the node hop secret: %w", err)
	}
	challenge, err := derive(cfg.SecretKey, challengeInfo)
	if err != nil {
		return nodes.HubConfig{}, fmt.Errorf("derive the node challenge secret: %w", err)
	}
	hc := nodes.HubConfig{
		Audience:        cfg.PublicURL,
		Replica:         replica,
		AdvertiseURL:    cfg.Nodes.AdvertiseURL,
		HopSecret:       hop,
		ChallengeSecret: challenge,
		HopInsecure:     cfg.Nodes.HopInsecure,
	}
	if cfg.Nodes.HopCAFile != "" {
		pem, err := os.ReadFile(cfg.Nodes.HopCAFile)
		if err != nil {
			return hc, fmt.Errorf("read nodes.hop_ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return hc, fmt.Errorf("nodes.hop_ca_file %s holds no PEM certificate", cfg.Nodes.HopCAFile)
		}
		hc.HopRootCAs = pool
	}
	return hc, nil
}
