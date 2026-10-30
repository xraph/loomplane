package firecracker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/xraph/loomplane/internal/secureconfig"
)

func RunHost(ctx context.Context, path string) error {
	var cfg HostConfig
	if err := secureconfig.ReadJSON(path, MaxControlBytes, true, &cfg); err != nil {
		return err
	}
	key, err := hex.DecodeString(cfg.HostKey)
	if err != nil || len(key) != 32 || len(cfg.Peers) == 0 {
		return ErrInvalid
	}
	for _, peer := range cfg.Peers {
		if len(peer.Fingerprint) != 64 || !identifier.MatchString(peer.Org) || (peer.Connection != "" && !identifier.MatchString(peer.Connection)) {
			return ErrInvalid
		}
	}
	if _, _, err = net.SplitHostPort(cfg.Listen); err != nil {
		return ErrInvalid
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLSCertificate, cfg.TLSKey)
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(cfg.ClientCA)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return ErrInvalid
	}
	if checkErr := prepareStateDirectory(cfg.StateDir); checkErr != nil {
		return checkErr
	}
	store, err := OpenStore(cfg.StateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	driver, err := NewDriver(cfg, store, key)
	if err != nil {
		return err
	}
	host := &Host{Store: store, Driver: driver, Limits: cfg.Limits, Key: key}
	srv := &http.Server{Addr: cfg.Listen, Handler: Handler{Host: host, Peers: cfg.Peers}, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 35 * time.Minute, WriteTimeout: 35 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 3)
	publisher := &Publisher{Host: host, Address: cfg.PublishAddress, Context: run}
	host.PreparePublish = publisher.Ensure
	go func() { result <- publisher.Run(run) }()
	if cfg.HTTPSListen != "" {
		ingress := &Ingress{Host: host, Config: cfg}
		go func() { result <- ingress.Run(run) }()
	}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			if e := host.Reconcile(run); e != nil {
				log.Print("Firecracker cleanup pending; inspect the ownership journal")
			}
			select {
			case <-run.Done():
				return
			case <-tick.C:
			}
		}
	}()
	if discovery, ok := driver.(interface{ RefreshDiscovery(context.Context) error }); ok {
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-run.Done():
					return
				case <-tick.C:
					call, stop := context.WithTimeout(run, 10*time.Second)
					if refreshErr := discovery.RefreshDiscovery(call); refreshErr != nil {
						log.Print("Firecracker service discovery refresh failed")
					}
					stop()
				}
			}
		}()
	}
	go func() { result <- srv.ListenAndServeTLS("", "") }()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-result:
	}
	cancel()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	shutdownErr := srv.Shutdown(closeCtx)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
		err = nil
	}
	return errors.Join(err, shutdownErr)
}
func HostUsage() error {
	return fmt.Errorf("usage: loomplane-host-agent /absolute/path/to/config.json")
}

// CheckHost verifies the configured Linux isolation prerequisites without
// starting listeners or workloads. The configuration remains administrator-owned.
func CheckHost(ctx context.Context, path string) (Capabilities, error) {
	var cfg HostConfig
	if err := secureconfig.ReadJSON(path, MaxControlBytes, true, &cfg); err != nil {
		return Capabilities{}, err
	}
	key, err := hex.DecodeString(cfg.HostKey)
	if err != nil {
		return Capabilities{}, err
	}
	driver, err := NewDriver(cfg, nil, key)
	if err != nil {
		return Capabilities{}, err
	}
	return driver.Check(ctx)
}
