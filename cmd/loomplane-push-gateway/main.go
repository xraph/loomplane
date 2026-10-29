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
	"math"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/xraph/loomplane/internal/buildinfo"
)

const (
	defaultAddr       = ":8790"
	defaultBundleID   = "com.loomplane.app"
	defaultServerRate = 50 // sends per second per server key
	defaultHandleRate = 30 // sends per minute per handle
)

var (
	sealingKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	appleIDPattern      = regexp.MustCompile(`^[A-Za-z0-9]{10}$`)
)

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
func run(_ context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
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
	if _, err := loadConfig(getenv); err != nil {
		fmt.Fprintf(stderr, "loomplane-push-gateway: %v\n", err)
		return 1
	}
	return 0
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
	if fcmFile != "" {
		fcm, err := loadFCM(getenv)
		if err != nil {
			errs = append(errs, err...)
		}
		c.FCM = fcm
	}
	if apnsFile != "" {
		apns, err := loadAPNs(getenv)
		if err != nil {
			errs = append(errs, err...)
		}
		c.APNs = apns
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
	} else if raw, err := readSecret(getenv, fileVar); err != nil {
		errs = append(errs, err)
	} else if key, err = base64.StdEncoding.DecodeString(string(raw)); err != nil || len(key) != 32 {
		errs = append(errs, fmt.Errorf("%s must hold 32 random bytes in base64 (openssl rand -base64 32)", fileVar))
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
	apns := &apnsConfig{
		KeyID:    getenv("PUSH_GATEWAY_APNS_KEY_ID"),
		TeamID:   getenv("PUSH_GATEWAY_APNS_TEAM_ID"),
		BundleID: getenv("PUSH_GATEWAY_APNS_BUNDLE_ID"),
	}
	if apns.BundleID == "" {
		apns.BundleID = defaultBundleID
	}
	raw, err := readSecret(getenv, name)
	if err != nil {
		errs = append(errs, err)
	} else if !isP8(raw) {
		errs = append(errs, fmt.Errorf("%s isn't an APNs .p8 key", name))
	} else {
		apns.PrivateKey = raw
	}
	if !appleIDPattern.MatchString(apns.KeyID) {
		errs = append(errs, errors.New("PUSH_GATEWAY_APNS_KEY_ID must be the 10-character key ID from Apple"))
	}
	if !appleIDPattern.MatchString(apns.TeamID) {
		errs = append(errs, errors.New("PUSH_GATEWAY_APNS_TEAM_ID must be the 10-character team ID from Apple"))
	}
	var urlErr error
	if apns.BaseURL, urlErr = baseURL(getenv, "PUSH_GATEWAY_APNS_BASE_URL"); urlErr != nil {
		errs = append(errs, urlErr)
	}
	return apns, errs
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
