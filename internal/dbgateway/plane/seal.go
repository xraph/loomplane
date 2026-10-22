package plane

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// ErrSealed is the one error Open returns. A bad size, a bad ephemeral key, a
// wrong key, a wrong context and a tampered value all look the same to the
// caller, so a failure says nothing about why it failed.
var ErrSealed = errors.New("sealed value is not trusted")

// sealInfoPrefix separates the seal key schedule from every other use of
// HKDF in the gateway. The caller's context follows it.
const sealInfoPrefix = "loomplane/dbgateway/seal/v1\x00"

// Sealed is a value encrypted to an X25519 public key: the sender's one-time
// public key, the AES-GCM nonce and the ciphertext with its tag.
type Sealed struct {
	Ephemeral  []byte `json:"ephemeral"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Seal encrypts plaintext so only the holder of the private key for `to` can
// read it. The context is bound to the ciphertext, so a value sealed for one
// purpose does not open for another. Seal fails if `to` is nil or not an
// X25519 key.
func Seal(to *ecdh.PublicKey, plaintext, context []byte) (Sealed, error) {
	if to == nil {
		return Sealed{}, errors.New("seal: nil recipient key")
	}
	if to.Curve() != ecdh.X25519() {
		return Sealed{}, errors.New("seal: recipient key is not X25519")
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Sealed{}, fmt.Errorf("seal: generate ephemeral key: %w", err)
	}
	aead, err := sealAEAD(eph, to, eph.PublicKey(), to, context)
	if err != nil {
		return Sealed{}, fmt.Errorf("seal: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("seal: generate nonce: %w", err)
	}
	return Sealed{
		Ephemeral:  eph.PublicKey().Bytes(),
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, context),
	}, nil
}

// Open decrypts a value sealed to key's public half under the same context.
// Every failure returns ErrSealed.
func Open(key *ecdh.PrivateKey, s Sealed, context []byte) ([]byte, error) {
	if key == nil || key.Curve() != ecdh.X25519() {
		return nil, ErrSealed
	}
	eph, err := ecdh.X25519().NewPublicKey(s.Ephemeral)
	if err != nil {
		return nil, ErrSealed
	}
	aead, err := sealAEAD(key, eph, eph, key.PublicKey(), context)
	if err != nil {
		return nil, ErrSealed
	}
	if len(s.Nonce) != aead.NonceSize() {
		return nil, ErrSealed
	}
	plaintext, err := aead.Open(nil, s.Nonce, s.Ciphertext, context)
	if err != nil {
		return nil, ErrSealed
	}
	return plaintext, nil
}

// sealAEAD runs X25519 between priv and peer, then derives the AES-256-GCM
// key with HKDF-SHA256. The salt is the ephemeral public key followed by the
// recipient public key, which both sides know; the info is the seal prefix
// followed by the context.
func sealAEAD(priv *ecdh.PrivateKey, peer, ephemeral, recipient *ecdh.PublicKey, context []byte) (cipher.AEAD, error) {
	shared, err := priv.ECDH(peer)
	if err != nil {
		return nil, err
	}
	salt := append(ephemeral.Bytes(), recipient.Bytes()...)
	info := append([]byte(sealInfoPrefix), context...)
	k, err := hkdf.Key(sha256.New, shared, salt, string(info), 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
