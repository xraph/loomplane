package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestDialSelectsPinnedHostAlgorithm(t *testing.T) {
	ed25519Host, _ := testKey(t)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaHost, err := ssh.NewSignerFromKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaHost, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaAlgorithms, ok := rsaHost.(ssh.AlgorithmSigner)
	if !ok {
		t.Fatal("RSA signer has no modern algorithms")
	}
	modernRSA, err := ssh.NewSignerWithAlgorithms(rsaAlgorithms, []string{ssh.KeyAlgoRSASHA256})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		pinned, other ssh.Signer
	}{{"ed25519 among ECDSA keys", ed25519Host, ecdsaHost}, {"RSA SHA256 without SHA1", modernRSA, ecdsaHost}} {
		t.Run(test.name, func(t *testing.T) {
			user, _ := testKey(t)
			listener, listenErr := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if listenErr != nil {
				t.Fatal(listenErr)
			}
			defer listener.Close()
			config := &ssh.ServerConfig{NoClientAuth: true}
			config.AddHostKey(test.other)
			config.AddHostKey(test.pinned)
			done := make(chan error, 1)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					done <- acceptErr
					return
				}
				defer conn.Close()
				remote, chans, requests, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					done <- handshakeErr
					return
				}
				defer remote.Close()
				go ssh.DiscardRequests(requests)
				go func() {
					for c := range chans {
						_ = c.Reject(ssh.Prohibited, "not used")
					}
				}()
				done <- nil
				_ = remote.Wait()
			}()
			client, dialErr := Dial(t.Context(), &Config{Address: listener.Addr().String(), User: "test", Socket: "/var/run/docker.sock", HostKey: test.pinned.PublicKey(), Signer: user})
			if dialErr != nil {
				t.Fatal(dialErr)
			}
			defer client.Close()
			if handshakeErr := <-done; handshakeErr != nil {
				t.Fatal(handshakeErr)
			}
		})
	}
}
