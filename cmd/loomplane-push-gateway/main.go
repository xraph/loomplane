// Command loomplane-push-gateway relays push notifications from Loomplane
// servers to FCM and APNs. It holds the app's platform credentials so a
// self-hosted server doesn't have to, and it keeps no state.
//
//	loomplane-push-gateway             run the gateway
//	loomplane-push-gateway --version   print the version
//
// It reads its settings from PUSH_GATEWAY_* environment variables. Secrets
// arrive as files named by *_FILE variables (Docker secrets), never as
// values, and no error or log line repeats what a secret file holds. Every
// variable is listed in apps/docs/content/docs/operations/push-notifications.mdx.
//
// Keep this package to this one file, like cmd/loomplane.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xraph/herald/driver/push"
	"github.com/xraph/herald/drivers/apns"
	"github.com/xraph/herald/pushgateway"

	"github.com/xraph/loomplane/internal/buildinfo"
)

const (
	defaultAddr       = ":8790"
	defaultBundleID   = "com.loomplane.app"
	defaultServerRate = 50 // sends per second per server key
	defaultHandleRate = 30 // sends per minute per device

	maxBody         = 64 << 10
	shutdownTimeout = 20 * time.Second
)

var (
	sealingKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	appleIDPattern      = regexp.MustCompile(`^[A-Z0-9]{10}$`)
)

// Variables that only mean something with their platform's key file.
var (
	fcmOnlyVars  = []string{"PUSH_GATEWAY_FCM_BASE_URL"}
	apnsOnlyVars = []string{
		"PUSH_GATEWAY_APNS_KEY_ID", "PUSH_GATEWAY_APNS_TEAM_ID",
		"PUSH_GATEWAY_APNS_BUNDLE_ID", "PUSH_GATEWAY_APNS_BASE_URL",
	}
)

// orphans reports each variable that is set while the file variable that
// enables its platform isn't, so a typo in the file variable fails loudly
// instead of silently turning the platform off.
func orphans(getenv func(string) string, fileVar string, vars []string) []error {
	if getenv(fileVar) != "" {
		return nil
	}
	var errs []error
	for _, v := range vars {
		if getenv(v) != "" {
			errs = append(errs, fmt.Errorf("%s is set but %s isn't", v, fileVar))
		}
	}
	return errs
}

type sealingKey struct {
	ID  string
	Key []byte
}

type fcmConfig struct {
	ServiceAccount     []byte
	ProjectID, BaseURL string
}

type apnsConfig struct {
	PrivateKey                       []byte
	KeyID, TeamID, BundleID, BaseURL string
}

type config struct {
	Addr        string
	SealingKeys []sealingKey // [0] current, [1] previous when set
	FCM         *fcmConfig   // nil when PUSH_GATEWAY_FCM_CREDENTIALS_FILE is unset
	APNs        *apnsConfig  // nil when PUSH_GATEWAY_APNS_KEY_FILE is unset
	ServerRate  float64      // per second
	HandleRate  float64      // per minute
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is the command; it returns the exit code.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loomplane-push-gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "usage: loomplane-push-gateway [--version]") }
	version := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	if *version {
		fmt.Fprintf(stdout, "loomplane-push-gateway %s\n", buildinfo.Version)
		return 0
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	c, err := loadConfig(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "loomplane-push-gateway: %v\n", err)
		return 1
	}
	var overrides []any
	if c.FCM != nil && c.FCM.BaseURL != "" {
		overrides = append(overrides, "fcm_base_url", c.FCM.BaseURL)
	}
	if c.APNs != nil && c.APNs.BaseURL != "" {
		overrides = append(overrides, "apns_base_url", c.APNs.BaseURL)
	}
	if len(overrides) > 0 {
		logger.Warn("endpoint override in use", overrides...)
	}
	gateway, err := pushgateway.New(gatewayConfig(c, logger))
	if err != nil {
		fmt.Fprintf(stderr, "loomplane-push-gateway: %v\n", err)
		return 1
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.Addr)
	if err != nil {
		fmt.Fprintf(stderr, "loomplane-push-gateway: %v\n", err)
		return 1
	}
	previous := ""
	if len(c.SealingKeys) > 1 {
		previous = c.SealingKeys[1].ID
	}
	logger.Info("push gateway listening",
		"addr", ln.Addr().String(),
		"version", buildinfo.Version,
		"sealing_key", c.SealingKeys[0].ID,
		"previous_sealing_key", previous,
		"android", c.FCM != nil,
		"ios", c.APNs != nil,
	)
	if err := serve(ctx, ln, newHandler(gateway), logger); err != nil {
		fmt.Fprintf(stderr, "loomplane-push-gateway: %v\n", err)
		return 1
	}
	logger.Info("push gateway stopped")
	return 0
}

// gatewayConfig maps the loaded settings onto Herald's gateway. It is the
// only code that names pushgateway types.
func gatewayConfig(c config, logger *slog.Logger) pushgateway.Config {
	g := pushgateway.Config{
		ServerPerSecond:  c.ServerRate,
		HandlePerMinute:  c.HandleRate,
		Logger:           logger,
		PlaceholderTitle: "Loomplane",
		PlaceholderBody:  "New notification",
	}
	for _, k := range c.SealingKeys {
		g.SealingKeys = append(g.SealingKeys, pushgateway.SealingKey{ID: k.ID, Key: k.Key})
	}
	if c.FCM != nil {
		settings := map[string]string{"project_id": c.FCM.ProjectID}
		if c.FCM.BaseURL != "" {
			settings["base_url"] = c.FCM.BaseURL
		}
		g.Android = &pushgateway.Platform{
			Driver:      &push.FCMDriver{},
			Credentials: map[string]string{"service_account": string(c.FCM.ServiceAccount)},
			Settings:    settings,
		}
	}
	if c.APNs != nil {
		settings := map[string]string{
			"key_id":    c.APNs.KeyID,
			"team_id":   c.APNs.TeamID,
			"bundle_id": c.APNs.BundleID,
		}
		if c.APNs.BaseURL != "" {
			settings["base_url"] = c.APNs.BaseURL
		}
		g.IOS = &pushgateway.Platform{
			Driver:      &apns.Driver{},
			Credentials: map[string]string{"private_key": string(c.APNs.PrivateKey)},
			Settings:    settings,
		}
	}
	return g
}

// newHandler routes the health check and the gateway's /v1/ API, and
// nothing else. Request bodies are capped at maxBody; any unread tail is
// drained up to that cap before the response flushes so a front proxy's
// body-size limit sees the full upload instead of an early 400.
func newHandler(gateway http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := io.WriteString(w, "ok\n"); err != nil {
			return
		}
	})
	mux.Handle("/v1/", http.MaxBytesHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.ServeHTTP(w, r)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
	}), maxBody))
	return mux
}

// serve runs h on ln until ctx is done, then lets in-flight requests finish
// for up to shutdownTimeout.
func serve(ctx context.Context, ln net.Listener, h http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// loadConfig reads and validates the environment. It reports every problem
// at once rather than the first.
func loadConfig(getenv func(string) string) (config, error) {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	c := config{Addr: defaultAddr}
	if v := getenv("PUSH_GATEWAY_ADDR"); v != "" {
		c.Addr = v
	}

	keys, keyErrs := loadSealingKeys(getenv)
	c.SealingKeys = keys
	errs = append(errs, keyErrs...)

	fcmFile := getenv("PUSH_GATEWAY_FCM_CREDENTIALS_FILE")
	apnsFile := getenv("PUSH_GATEWAY_APNS_KEY_FILE")
	if fcmFile == "" && apnsFile == "" {
		fail("set PUSH_GATEWAY_FCM_CREDENTIALS_FILE, PUSH_GATEWAY_APNS_KEY_FILE or both")
	}
	errs = append(errs, orphans(getenv, "PUSH_GATEWAY_FCM_CREDENTIALS_FILE", fcmOnlyVars)...)
	errs = append(errs, orphans(getenv, "PUSH_GATEWAY_APNS_KEY_FILE", apnsOnlyVars)...)
	if fcmFile != "" {
		fcm, err := loadFCM(getenv)
		if err != nil {
			errs = append(errs, err...)
		}
		c.FCM = fcm
	}
	if apnsFile != "" {
		apnsCfg, err := loadAPNs(getenv)
		if err != nil {
			errs = append(errs, err...)
		}
		c.APNs = apnsCfg
	}

	var err error
	if c.ServerRate, err = rate(getenv, "PUSH_GATEWAY_SERVER_RATE", defaultServerRate); err != nil {
		errs = append(errs, err)
	}
	if c.HandleRate, err = rate(getenv, "PUSH_GATEWAY_HANDLE_RATE", defaultHandleRate); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return config{}, errors.Join(errs...)
	}
	return c, nil
}

func loadSealingKeys(getenv func(string) string) ([]sealingKey, []error) {
	var errs []error
	var keys []sealingKey
	current, err := sealingKeyFrom(getenv, "PUSH_GATEWAY_SEALING_KEY_ID", "PUSH_GATEWAY_SEALING_KEY_FILE", true)
	if err != nil {
		errs = append(errs, err...)
	} else {
		keys = append(keys, current)
	}
	prevID := getenv("PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID")
	prevFile := getenv("PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE")
	switch {
	case prevID == "" && prevFile == "":
	case prevID == "" || prevFile == "":
		errs = append(errs, errors.New("set PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID and PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE together"))
	case prevID == getenv("PUSH_GATEWAY_SEALING_KEY_ID"):
		errs = append(errs, errors.New("PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID must differ from PUSH_GATEWAY_SEALING_KEY_ID"))
	default:
		prev, err := sealingKeyFrom(getenv, "PUSH_GATEWAY_PREVIOUS_SEALING_KEY_ID", "PUSH_GATEWAY_PREVIOUS_SEALING_KEY_FILE", false)
		if err != nil {
			errs = append(errs, err...)
		} else if len(errs) == 0 {
			keys = append(keys, prev)
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return keys, nil
}

func sealingKeyFrom(getenv func(string) string, idVar, fileVar string, required bool) (sealingKey, []error) {
	var errs []error
	id := getenv(idVar)
	switch {
	case id == "" && required:
		errs = append(errs, fmt.Errorf("%s is required", idVar))
	case id != "" && !sealingKeyIDPattern.MatchString(id):
		errs = append(errs, fmt.Errorf(`%s must be 1 to 32 letters, digits, "-" or "_"`, idVar))
	}
	var key []byte
	if getenv(fileVar) == "" {
		if required {
			errs = append(errs, fmt.Errorf("%s is required", fileVar))
		}
	} else {
		raw, err := readSecret(getenv, fileVar)
		switch {
		case err != nil:
			errs = append(errs, err)
		default:
			key, err = base64.StdEncoding.DecodeString(string(raw))
			if err != nil || len(key) != 32 {
				errs = append(errs, fmt.Errorf("%s must hold 32 random bytes in base64 (openssl rand -base64 32)", fileVar))
			}
		}
	}
	if len(errs) > 0 {
		return sealingKey{}, errs
	}
	return sealingKey{ID: id, Key: key}, nil
}

func loadFCM(getenv func(string) string) (*fcmConfig, []error) {
	const name = "PUSH_GATEWAY_FCM_CREDENTIALS_FILE"
	var errs []error
	fcm := &fcmConfig{}
	raw, err := readSecret(getenv, name)
	if err != nil {
		errs = append(errs, err)
	} else {
		var sa struct {
			Type        string `json:"type"`
			ProjectID   string `json:"project_id"`
			PrivateKey  string `json:"private_key"`
			ClientEmail string `json:"client_email"`
		}
		if json.Unmarshal(raw, &sa) != nil || sa.Type != "service_account" ||
			sa.ProjectID == "" || sa.PrivateKey == "" || sa.ClientEmail == "" {
			errs = append(errs, fmt.Errorf("%s isn't a Firebase service-account key", name))
		} else {
			fcm.ServiceAccount, fcm.ProjectID = raw, sa.ProjectID
		}
	}
	var urlErr error
	if fcm.BaseURL, urlErr = baseURL(getenv, "PUSH_GATEWAY_FCM_BASE_URL"); urlErr != nil {
		errs = append(errs, urlErr)
	}
	return fcm, errs
}

func loadAPNs(getenv func(string) string) (*apnsConfig, []error) {
	const name = "PUSH_GATEWAY_APNS_KEY_FILE"
	var errs []error
	cfg := &apnsConfig{
		KeyID:    getenv("PUSH_GATEWAY_APNS_KEY_ID"),
		TeamID:   getenv("PUSH_GATEWAY_APNS_TEAM_ID"),
		BundleID: getenv("PUSH_GATEWAY_APNS_BUNDLE_ID"),
	}
	if cfg.BundleID == "" {
		cfg.BundleID = defaultBundleID
	}
	raw, err := readSecret(getenv, name)
	switch {
	case err != nil:
		errs = append(errs, err)
	case !isP8(raw):
		errs = append(errs, fmt.Errorf("%s isn't an APNs .p8 key", name))
	default:
		cfg.PrivateKey = raw
	}
	if !appleIDPattern.MatchString(cfg.KeyID) {
		errs = append(errs, errors.New("PUSH_GATEWAY_APNS_KEY_ID must be the 10-character key ID from Apple"))
	}
	if !appleIDPattern.MatchString(cfg.TeamID) {
		errs = append(errs, errors.New("PUSH_GATEWAY_APNS_TEAM_ID must be the 10-character team ID from Apple"))
	}
	var urlErr error
	if cfg.BaseURL, urlErr = baseURL(getenv, "PUSH_GATEWAY_APNS_BASE_URL"); urlErr != nil {
		errs = append(errs, urlErr)
	}
	return cfg, errs
}

// isP8 reports whether raw is a PKCS#8 PEM holding a P-256 key, which is
// what Apple's .p8 files are.
func isP8(raw []byte) bool {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(string(raw), "\r\n", "\n")))
	if block == nil || block.Type != "PRIVATE KEY" {
		return false
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	return ok && ec.Curve == elliptic.P256()
}

// readSecret reads the file the variable names and trims surrounding
// whitespace. Its errors name the variable and the path, never the content.
func readSecret(getenv func(string) string, name string) ([]byte, error) {
	b, err := os.ReadFile(getenv(name))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return []byte(strings.TrimSpace(string(b))), nil
}

func baseURL(getenv func(string) string, name string) (string, error) {
	v := getenv(name)
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s must be an http or https URL", name)
	}
	return v, nil
}

func rate(getenv func(string) string, name string, def float64) (float64, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, fmt.Errorf("%s must be a positive number", name)
	}
	return f, nil
}
