package catalogconnect

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	EngineElasticsearch = "elasticsearch"
	EngineOpenSearch    = "opensearch"
	EngineFalkorDB      = "falkordb"
	maxEngineAccess     = 96 * 1024
)

// EngineAccess is write-only access material for a checked external engine.
// Keep the encoded value in Vault; resolve fields only while preparing a workload.
type EngineAccess struct {
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
	TLS      string `json:"tls"`
	CACert   string `json:"ca_cert,omitempty"`
}

// Format keeps credentials out of formatted diagnostics, including %#v.
func (EngineAccess) Format(w fmt.State, _ rune) {
	if _, err := io.WriteString(w, "[engine access redacted]"); err != nil {
		return
	}
}

// IsEngineAccessEngine includes only engines with an implemented native prober.
func IsEngineAccessEngine(engine string) bool {
	switch engine {
	case EngineElasticsearch, EngineOpenSearch, EngineFalkorDB:
		return true
	}
	return false
}

func invalidEngineAccess() error {
	return fmt.Errorf("%w: supply one engine host, application username and password, and a supported TLS mode with certificate-only inline CA data", ErrInvalid)
}

// EncodeEngineAccess validates and encodes the Vault contract.
func EncodeEngineAccess(access EngineAccess) (string, error) {
	if err := normalizeEngineAccess(&access); err != nil {
		return "", err
	}
	raw, err := json.Marshal(access) //nolint:gosec // This write-only credential contract is stored only in Vault.
	if err != nil || len(raw) > maxEngineAccess {
		return "", invalidEngineAccess()
	}
	return string(raw), nil
}

// ParseEngineAccess rejects unknown, duplicate, case-variant and nested fields.
func ParseEngineAccess(raw string) (EngineAccess, error) {
	var access EngineAccess
	if len(raw) > maxEngineAccess || !utf8.ValidString(raw) {
		return access, invalidEngineAccess()
	}
	d := json.NewDecoder(strings.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return access, invalidEngineAccess()
	}
	seen := map[string]bool{}
	for d.More() {
		key, keyErr := d.Token()
		name, ok := key.(string)
		if keyErr != nil || !ok || seen[name] {
			return EngineAccess{}, invalidEngineAccess()
		}
		seen[name] = true
		var value any
		switch name {
		case "engine":
			value = &access.Engine
		case "host":
			value = &access.Host
		case "port":
			value = &access.Port
		case "username":
			value = &access.Username
		case "password":
			value = &access.Password
		case "tls":
			value = &access.TLS
		case "ca_cert":
			value = &access.CACert
		default:
			return EngineAccess{}, invalidEngineAccess()
		}
		// null is not a meaningful value for any field in this flat contract.
		var field json.RawMessage
		if d.Decode(&field) != nil || strings.TrimSpace(string(field)) == "null" || json.Unmarshal(field, value) != nil {
			return EngineAccess{}, invalidEngineAccess()
		}
	}
	if end, endErr := d.Token(); endErr != nil || end != json.Delim('}') {
		return EngineAccess{}, invalidEngineAccess()
	}
	if _, tailErr := d.Token(); tailErr != io.EOF {
		return EngineAccess{}, invalidEngineAccess()
	}
	if err := normalizeEngineAccess(&access); err != nil {
		return EngineAccess{}, err
	}
	return access, nil
}

func normalizeEngineAccess(a *EngineAccess) error {
	if !IsEngineAccessEngine(a.Engine) || !engineHost(a.Host) || !engineCredential(a.Username, 256) || !engineCredential(a.Password, 4096) || a.Engine != EngineFalkorDB && strings.Contains(a.Username, ":") {
		return invalidEngineAccess()
	}
	if a.Port == 0 {
		a.Port = 9200
		if a.Engine == EngineFalkorDB {
			a.Port = 6379
		}
	}
	if a.Port < 1 || a.Port > 65535 {
		return invalidEngineAccess()
	}
	if a.TLS != TLSDisable && a.TLS != TLSVerifyFull || a.Engine == EngineOpenSearch && a.TLS != TLSVerifyFull || a.TLS == TLSDisable && a.CACert != "" {
		return invalidEngineAccess()
	}
	if _, err := engineRootCAs(a.CACert); err != nil {
		return err
	}
	return nil
}

func engineCredential(s string, limit int) bool {
	if s == "" || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func engineHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func engineRootCAs(raw string) (*x509.CertPool, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 64*1024 {
		return nil, invalidEngineAccess()
	}
	const begin = "-----BEGIN CERTIFICATE-----"
	const end = "-----END CERTIFICATE-----"
	roots := x509.NewCertPool()
	rest := strings.TrimSpace(raw)
	count := 0
	for rest != "" {
		header, content, ok := strings.Cut(rest, "\n")
		if !ok || strings.TrimRight(header, " \t\r") != begin {
			return nil, invalidEngineAccess()
		}
		body, afterEnd, ok := strings.Cut(content, "\n"+end)
		endSuffix, tail, _ := strings.Cut(afterEnd, "\n")
		if !ok || strings.TrimSpace(endSuffix) != "" || strings.Contains(body, "-----BEGIN") {
			return nil, invalidEngineAccess()
		}
		// Decode one bounded block. The PEM decoder can skip a broken
		// prefix when a later certificate is present.
		block, remaining := pem.Decode([]byte(rest[:len(rest)-len(tail)]))
		if block == nil || len(remaining) != 0 || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, invalidEngineAccess()
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, invalidEngineAccess()
		}
		count++
		if count > 8 {
			return nil, invalidEngineAccess()
		}
		roots.AddCert(cert)
		rest = strings.TrimSpace(tail)
	}
	if count == 0 {
		return nil, invalidEngineAccess()
	}
	return roots, nil
}

func parseEngineEndpoint(e *Endpoint, raw string) error {
	a, err := ParseEngineAccess(raw)
	if err != nil || a.Engine != e.Engine {
		return invalidEngineAccess()
	}
	e.Host, e.Port, e.Username, e.Database, e.TLS = a.Host, a.Port, a.Username, "", a.TLS
	return nil
}

func engineEndpointMatches(e *Endpoint, raw string) bool {
	expected := &Endpoint{Engine: e.Engine}
	return parseEngineEndpoint(expected, raw) == nil && e.Host == expected.Host && e.Port == expected.Port && e.Username == expected.Username && e.Database == expected.Database && e.TLS == expected.TLS
}

// EngineAccessField resolves only normalized public connection field names.
// Its return value can contain workload credentials and must stay secret.
func EngineAccessField(raw, field string) (string, error) {
	a, err := ParseEngineAccess(raw)
	if err != nil {
		return "", err
	}
	switch field {
	case "host":
		return a.Host, nil
	case "port":
		return strconv.Itoa(a.Port), nil
	case "hostport":
		return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)), nil
	case "username":
		return a.Username, nil
	case "password":
		return a.Password, nil
	case "database", "query":
		return "", nil
	case "ca_cert":
		return a.CACert, nil
	case "tls_mode":
		if a.Engine == EngineFalkorDB {
			return a.TLS, nil
		}
	case "url":
		authority := net.JoinHostPort(a.Host, strconv.Itoa(a.Port))
		if a.Engine == EngineFalkorDB {
			return authority, nil
		}
		scheme := "https"
		if a.TLS == TLSDisable {
			scheme = "http"
		}
		return (&url.URL{Scheme: scheme, Host: authority}).String(), nil
	}
	return "", fmt.Errorf("%w: the engine connection does not expose that field", ErrInvalid)
}

// RedactEngineAccess removes engine credentials and inline CA data from diagnostics.
// You can also pass legacy DSNs; they leave the message unchanged.
func RedactEngineAccess(msg, raw string) string {
	if len(raw) > maxEngineAccess {
		return msg
	}
	// Decode bounded fields even if the envelope failed validation.
	var a EngineAccess
	if json.Unmarshal([]byte(raw), &a) != nil || !IsEngineAccessEngine(a.Engine) {
		return msg
	}
	values := make([]string, 0, 24)
	for _, secret := range []string{a.Password, a.CACert, base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password))} {
		if secret == "" {
			continue
		}
		escaped, err := json.Marshal(secret)
		if err != nil {
			return "[redacted]"
		}
		values = append(values, secret, url.QueryEscape(secret), url.PathEscape(secret), string(escaped[1:len(escaped)-1]), base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)))
	}
	// Remove complete encoded values before a short password can alter them.
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, value := range values {
		if value != "" {
			msg = strings.ReplaceAll(msg, value, "[redacted]")
		}
	}
	return msg
}
