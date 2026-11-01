package catalogconnect

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Bucket engines use structured access material, not database URLs.
const (
	EngineS3  = "s3"
	EngineGCS = "gcs"
)

// BucketAccess holds explicitly supplied application credentials. Store it only in Vault.
type BucketAccess struct {
	Provider           string `json:"provider,omitempty"`
	Bucket             string `json:"bucket,omitempty"`
	Region             string `json:"region,omitempty"`
	ExpectedOwner      string `json:"expected_owner,omitempty"`
	AccessKeyID        string `json:"access_key_id,omitempty"`
	SecretAccessKey    string `json:"secret_access_key,omitempty"`
	SessionToken       string `json:"session_token,omitempty"`
	ServiceAccountJSON string `json:"service_account_json,omitempty"`
}

var (
	bucketLocationPattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	googleBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*[a-z0-9]$`)
	bucketNamePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	bucketRegionPattern     = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-\d+$`)
	bucketOwnerPattern      = regexp.MustCompile(`^\d{12}$`)
	bucketAccessKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
)

func invalidBucketAccess() error {
	return fmt.Errorf("%w: supply matching S3 application keys or a Google service-account key for this bucket", ErrInvalid)
}

// EncodeBucketAccess validates and encodes the write-only access contract.
func EncodeBucketAccess(access BucketAccess) (string, error) {
	if err := validateBucketAccess(access); err != nil {
		return "", err
	}
	data, err := json.Marshal(access) //nolint:gosec // This write-only credential contract is stored only in Vault.
	if err != nil {
		return "", invalidBucketAccess()
	}
	return string(data), nil
}

// ParseBucketAccess accepts only the supported credential fields and providers.
func ParseBucketAccess(raw string) (BucketAccess, error) {
	var access BucketAccess
	if len(raw) > 128*1024 || strictBucketJSON(raw, &access) != nil {
		return BucketAccess{}, invalidBucketAccess()
	}
	if err := validateBucketAccess(access); err != nil {
		return BucketAccess{}, err
	}
	return access, nil
}

// ValidBucketName checks the standard bucket name rules for the selected provider.
func ValidBucketName(provider, name string) bool {
	if provider != "aws" && provider != "gcp" || net.ParseIP(name) != nil {
		return false
	}
	if provider == "aws" && (strings.HasSuffix(name, "--x-s3") || strings.HasSuffix(name, "--table-s3")) {
		return false
	}
	return validBucketName(provider, name)
}

func validBucketName(provider, name string) bool {
	if provider != "gcp" {
		return bucketNamePattern.MatchString(name) && !strings.Contains(name, "..") && !strings.Contains(name, ".-") && !strings.Contains(name, "-.")
	}
	if len(name) < 3 || len(name) > 222 || !googleBucketNamePattern.MatchString(name) {
		return false
	}
	for _, component := range strings.Split(name, ".") {
		if component == "" || len(component) > 63 {
			return false
		}
	}
	return true
}

func validateBucketAccess(a BucketAccess) error {
	if !ValidBucketName(a.Provider, a.Bucket) || !bucketLocationPattern.MatchString(a.Region) || len(a.Region) > 64 {
		return invalidBucketAccess()
	}
	switch a.Provider {
	case "aws":
		if !bucketRegionPattern.MatchString(a.Region) || strings.HasPrefix(a.Region, "cn-") || !bucketOwnerPattern.MatchString(a.ExpectedOwner) || !bucketAccessKeyPattern.MatchString(a.AccessKeyID) || len(a.SecretAccessKey) < 16 || len(a.SecretAccessKey) > 1024 || len(a.SessionToken) > 16384 || a.ServiceAccountJSON != "" {
			return invalidBucketAccess()
		}
		for _, field := range []string{a.SecretAccessKey, a.SessionToken} {
			if strings.ContainsAny(field, "\r\n\x00") {
				return invalidBucketAccess()
			}
		}
	case "gcp":
		if a.AccessKeyID != "" || a.SecretAccessKey != "" || a.SessionToken != "" || a.ExpectedOwner != "" {
			return invalidBucketAccess()
		}
		if _, err := parseBucketServiceAccount(a.ServiceAccountJSON); err != nil {
			return invalidBucketAccess()
		}
	default:
		return invalidBucketAccess()
	}
	return nil
}

type bucketServiceAccount struct {
	Type                       string `json:"type"`
	ProjectID                  string `json:"project_id"`
	PrivateKeyID               string `json:"private_key_id"`
	PrivateKey                 string `json:"private_key"`
	ClientEmail                string `json:"client_email"`
	ClientID                   string `json:"client_id"`
	AuthURI                    string `json:"auth_uri"`
	TokenURI                   string `json:"token_uri"`
	AuthProviderCertificateURL string `json:"auth_provider_x509_cert_url"`
	ClientCertificateURL       string `json:"client_x509_cert_url"`
	UniverseDomain             string `json:"universe_domain"`
}

func parseBucketServiceAccount(raw string) (*bucketServiceAccount, error) {
	var account bucketServiceAccount
	if len(raw) > 96*1024 || strictBucketJSON(raw, &account) != nil || account.Type != "service_account" || account.ProjectID == "" || !strings.HasSuffix(account.ClientEmail, ".gserviceaccount.com") || strings.ContainsAny(account.ClientEmail, "\r\n\x00 ") || account.TokenURI != "https://oauth2.googleapis.com/token" || account.UniverseDomain != "" && account.UniverseDomain != "googleapis.com" {
		return nil, invalidBucketAccess()
	}
	if account.AuthURI != "" && account.AuthURI != "https://accounts.google.com/o/oauth2/auth" && account.AuthURI != "https://accounts.google.com/o/oauth2/v2/auth" {
		return nil, invalidBucketAccess()
	}
	if account.AuthProviderCertificateURL != "" && account.AuthProviderCertificateURL != "https://www.googleapis.com/oauth2/v1/certs" {
		return nil, invalidBucketAccess()
	}
	if account.ClientCertificateURL != "" {
		certificateURL, parseErr := url.Parse(account.ClientCertificateURL)
		if parseErr != nil || certificateURL.Scheme != "https" || certificateURL.Host != "www.googleapis.com" || certificateURL.User != nil || certificateURL.RawQuery != "" || certificateURL.Fragment != "" || certificateURL.Path != "/robot/v1/metadata/x509/"+account.ClientEmail {
			return nil, invalidBucketAccess()
		}
	}
	block, rest := pem.Decode([]byte(account.PrivateKey))
	if block == nil || strings.TrimSpace(string(rest)) != "" {
		return nil, invalidBucketAccess()
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, invalidBucketAccess()
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok || rsaKey.N.BitLen() < 2048 || rsaKey.N.BitLen() > 4096 {
		return nil, invalidBucketAccess()
	}
	return &account, nil
}

// strictBucketJSON also rejects duplicate fields, including nested objects.
func strictBucketJSON(raw string, out any) error {
	tokens := json.NewDecoder(strings.NewReader(raw))
	if err := uniqueBucketValue(tokens); err != nil {
		return err
	}
	if _, err := tokens.Token(); !errors.Is(err, io.EOF) {
		return invalidBucketAccess()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func uniqueBucketValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			field, fieldErr := d.Token()
			if fieldErr != nil {
				return fieldErr
			}
			name, ok := field.(string)
			if !ok || seen[name] {
				return invalidBucketAccess()
			}
			seen[name] = true
			if valueErr := uniqueBucketValue(d); valueErr != nil {
				return valueErr
			}
		}
	case '[':
		for d.More() {
			if valueErr := uniqueBucketValue(d); valueErr != nil {
				return valueErr
			}
		}
	default:
		return invalidBucketAccess()
	}
	_, err = d.Token()
	return err
}

func parseBucketEndpoint(endpoint *Endpoint, raw string) error {
	access, err := ParseBucketAccess(raw)
	if err != nil {
		return err
	}
	if endpoint.Engine == EngineS3 && access.Provider != "aws" || endpoint.Engine == EngineGCS && access.Provider != "gcp" {
		return invalidBucketAccess()
	}
	endpoint.Host, endpoint.Port, endpoint.Database, endpoint.TLS = bucketHost(access), 443, access.Bucket, TLSVerifyFull
	return nil
}
func bucketHost(a BucketAccess) string {
	if a.Provider == "aws" {
		return "s3." + a.Region + ".amazonaws.com"
	}
	return "storage.googleapis.com"
}

// BucketEnvironment lists the output names available to a bucket consumer.
func BucketEnvironment(engine string) []string {
	out := []string{"STORAGE_URL", "STORAGE_BUCKET", "STORAGE_REGION", "STORAGE_ENDPOINT"}
	switch engine {
	case EngineS3:
		return append(out, "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_S3_EXPECTED_BUCKET_OWNER")
	case EngineGCS:
		return append(out, "GOOGLE_SERVICE_ACCOUNT_JSON")
	default:
		return nil
	}
}

// BucketOutputs returns workload outputs. Their credential values must stay secret.
func BucketOutputs(raw string) (map[string]string, error) {
	a, err := ParseBucketAccess(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]string{"STORAGE_BUCKET": a.Bucket, "STORAGE_REGION": a.Region, "STORAGE_ENDPOINT": "https://" + bucketHost(a)}
	switch a.Provider {
	case "aws":
		out["STORAGE_URL"] = "s3://" + a.Bucket + "?region=" + url.QueryEscape(a.Region)
		out["AWS_ACCESS_KEY_ID"], out["AWS_SECRET_ACCESS_KEY"], out["AWS_SESSION_TOKEN"], out["AWS_REGION"], out["AWS_S3_EXPECTED_BUCKET_OWNER"] = a.AccessKeyID, a.SecretAccessKey, a.SessionToken, a.Region, a.ExpectedOwner
	case "gcp":
		out["STORAGE_URL"], out["GOOGLE_SERVICE_ACCOUNT_JSON"] = "gs://"+a.Bucket, a.ServiceAccountJSON
	}
	return out, nil
}

func redactBucketAccess(msg, raw string) string {
	var access BucketAccess
	if json.Unmarshal([]byte(raw), &access) != nil {
		return msg
	}
	values := []string{access.AccessKeyID, access.SecretAccessKey, access.SessionToken, access.ServiceAccountJSON}
	var key bucketServiceAccount
	if json.Unmarshal([]byte(access.ServiceAccountJSON), &key) == nil {
		values = append(values, key.PrivateKey, key.PrivateKeyID)
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, v := range values {
		if v == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, v, "[redacted]")
		escaped, escapeErr := json.Marshal(v)
		if escapeErr == nil && len(escaped) > 2 {
			msg = strings.ReplaceAll(msg, string(escaped[1:len(escaped)-1]), "[redacted]")
		}
	}
	return msg
}
