package firecracker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xraph/loomplane/internal/secureconfig"
)

func RunHost(ctx context.Context, path string) error {
	var cfg HostConfig
	if err := secureconfig.ReadJSON(path, MaxControlBytes, true, &cfg); err != nil {
		return err
	}
	if err := ValidatePlatform(cfg); err != nil {
		return err
	}
	key, err := hex.DecodeString(cfg.HostKey)
	if err != nil || len(key) != 32 || (len(cfg.Peers) == 0 && cfg.Platform == nil) {
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
	roots, err := readClientCA(cfg.ClientCA)
	if err != nil {
		return err
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
	caps, err := driver.Check(ctx)
	if err != nil {
		return err
	}
	if checkErr := platformReady(cfg, caps); checkErr != nil {
		return checkErr
	}
	host := &Host{Store: store, Driver: driver, Limits: cfg.Limits, Key: key}
	var platform *PlatformTrust
	if cfg.Platform != nil {
		platform = NewPlatformTrust(cfg.Platform.Trusted()...)
	}
	var clientCAs atomic.Pointer[x509.CertPool]
	clientCAs.Store(roots)
	// NextProtos is set here because ListenAndServeTLS adds h2 only to its
	// own copy, which GetConfigForClient's answers don't inherit.
	base := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
		NextProtos: []string{"h2", "http/1.1"}}
	tlsConfig := base.Clone()
	// Each handshake verifies against the client CAs loaded last, so a
	// reload takes effect for new connections without a restart.
	tlsConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		c := base.Clone()
		c.ClientCAs = clientCAs.Load()
		return c, nil
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: Handler{Host: host, Peers: cfg.Peers, Platform: platform}, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 35 * time.Minute, WriteTimeout: 35 * time.Minute, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	// SIGHUP reloads the platform trust. It is caught on every host, so a
	// reload sent to a per-org host can't stop it.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		current := cfg
		for {
			select {
			case <-run.Done():
				return
			case <-hup:
			}
			if platform == nil {
				log.Print("reload ignored: only a platform host reloads its trust; restart the agent to apply other changes")
				continue
			}
			next, pool, reloadErr := ReloadPlatformTrust(path, current)
			if reloadErr != nil {
				log.Printf("platform trust not reloaded: %v", reloadErr)
				continue
			}
			clientCAs.Store(pool)
			platform.Store(next.Platform.Trusted())
			current = next
			log.Printf("platform trust reloaded: %d platform certificates trusted", len(next.Platform.Trusted()))
		}
	}()
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

// readClientCA reads the PEM certificates a client may present a chain to.
// A platform host's file holds the platform's self-signed client
// certificates themselves.
func readClientCA(path string) (*x509.CertPool, error) {
	roots, _, err := readClientCerts(path)
	return roots, err
}

// readClientCerts is readClientCA with the SHA-256 fingerprints of the
// certificates in the file.
func readClientCerts(path string) (*x509.CertPool, map[string]bool, error) {
	ca, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	roots := x509.NewCertPool()
	fps := map[string]bool{}
	for rest := ca; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		// A block that doesn't parse is skipped, as AppendCertsFromPEM did.
		cert, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			continue
		}
		roots.AddCert(cert)
		sum := sha256.Sum256(cert.Raw)
		fps[hex.EncodeToString(sum[:])] = true
	}
	if len(fps) == 0 {
		return nil, nil, ErrInvalid
	}
	return roots, fps, nil
}

// errReloadScope refuses a reload that changes more than the platform trust.
var errReloadScope = fmt.Errorf("%w: a reload may change only the platform fingerprints and the client CA file's contents; restart the agent for anything else", ErrInvalid)

// ReloadPlatformTrust reads the configuration at path again for a running
// platform host whose configuration is current. Only the platform peer's
// Fingerprint and Next, and the contents of the client CA file, may differ,
// and the runtime identity must come out the same, so no VM, route or
// tunnel bound to it is cut off. It returns the new configuration and client
// CAs for the caller to swap in.
func ReloadPlatformTrust(path string, current HostConfig) (HostConfig, *x509.CertPool, error) {
	var next HostConfig
	if err := secureconfig.ReadJSON(path, MaxControlBytes, true, &next); err != nil {
		return HostConfig{}, nil, err
	}
	if current.Platform == nil || next.Platform == nil {
		return HostConfig{}, nil, errReloadScope
	}
	if err := ValidatePlatform(next); err != nil {
		return HostConfig{}, nil, err
	}
	a, b := current, next
	a.Platform, b.Platform = nil, nil
	if !reflect.DeepEqual(a, b) {
		return HostConfig{}, nil, errReloadScope
	}
	was, err := identityInput(current)
	if err != nil {
		return HostConfig{}, nil, err
	}
	now, err := identityInput(next)
	if err != nil {
		return HostConfig{}, nil, err
	}
	if !bytes.Equal(was, now) {
		return HostConfig{}, nil, fmt.Errorf("%w: the runtime identity would change; when the platform fingerprint changes, set Identity to the fingerprint the host started with", ErrInvalid)
	}
	pool, inFile, err := readClientCerts(next.ClientCA)
	if err != nil {
		return HostConfig{}, nil, err
	}
	// A trusted fingerprint with no certificate in the file could never
	// connect: the platform would be locked out, or a rotation would stall.
	for _, fp := range next.Platform.Trusted() {
		if !inFile[fp] {
			return HostConfig{}, nil, fmt.Errorf("%w: platform fingerprint %s has no certificate in the client CA file", ErrInvalid, fp[:12])
		}
	}
	return next, pool, nil
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
	if err := ValidatePlatform(cfg); err != nil {
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
	caps, err := driver.Check(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	if checkErr := platformReady(cfg, caps); checkErr != nil {
		return Capabilities{}, checkErr
	}
	return caps, nil
}

// platformReady refuses a platform host that shares cores between hardware
// threads, since co-resident tenants could then read each other's data.
func platformReady(cfg HostConfig, c Capabilities) error {
	if cfg.Platform != nil && c.SMTActive {
		return fmt.Errorf("%w: a platform host must run with SMT off", ErrUnavailable)
	}
	return nil
}
