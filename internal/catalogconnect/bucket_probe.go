package catalogconnect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"

	"github.com/xraph/loomplane/internal/recipes"
)

// BucketProber checks bucket listing access using only explicit workload credentials.
// RootCAs optionally sets the certificate trust used for the HTTPS check.
type BucketProber struct{ RootCAs *x509.CertPool }

var _ Prober = BucketProber{}

// Probe performs signed read-only requests. It never creates or reads object contents.
func (p BucketProber) Probe(ctx context.Context, dial DialFunc, raw string, req recipes.Requirement) (*Report, error) {
	a, err := ParseBucketAccess(raw)
	if err != nil {
		return nil, err
	}
	engine := EngineS3
	if a.Provider == "cloudflare" {
		engine = EngineR2
	}
	if a.Provider == "gcp" {
		engine = EngineGCS
	}
	if req.Engine != "" && req.Engine != engine {
		return nil, invalidBucketAccess()
	}
	if dial == nil {
		return nil, errors.New("the bucket check requires a guarded dialer")
	}
	if req.MinVersion != "" || len(req.Extensions) > 0 || len(req.Preload) > 0 || req.CreateRole {
		return &Report{Problems: []string{"bucket access cannot satisfy database version, extension or role requirements"}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.RootCAs}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: probeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if a.Provider == "aws" || a.Provider == "cloudflare" {
		err = probeS3Bucket(ctx, client, a)
	} else {
		err = probeGoogleBucket(ctx, client, a)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errTimedOut
		}
		return nil, errors.New("the bucket access check failed; verify its application credentials, owner, region and network")
	}
	return &Report{Version: engine}, nil
}

func probeS3Bucket(ctx context.Context, client *http.Client, a BucketAccess) error {
	endpoint := "https://" + bucketHost(a) + "/" + url.PathEscape(a.Bucket)
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		target := endpoint
		if method == http.MethodGet {
			target += "?list-type=2&max-keys=1"
		}
		request, err := http.NewRequestWithContext(ctx, method, target, http.NoBody)
		if err != nil {
			return err
		}
		if a.Provider == "aws" {
			request.Header.Set("X-Amz-Expected-Bucket-Owner", a.ExpectedOwner)
		}
		credentials := aws.Credentials{AccessKeyID: a.AccessKeyID, SecretAccessKey: a.SecretAccessKey, SessionToken: a.SessionToken}
		const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		if signErr := v4.NewSigner().SignHTTP(ctx, credentials, request, emptySHA256, "s3", a.Region, time.Now()); signErr != nil {
			return signErr
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		err = readS3BucketResponse(response, a, method)
		closeErr := response.Body.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func readS3BucketResponse(response *http.Response, a BucketAccess, method string) error {
	region := response.Header.Get("X-Amz-Bucket-Region")
	if response.StatusCode != http.StatusOK || (a.Provider == "aws" && method == http.MethodHead && region != a.Region) || (region != "" && region != a.Region) {
		return errors.New("bucket owner, region or access was refused")
	}
	if method == http.MethodHead {
		return nil
	}
	var listed struct {
		XMLName xml.Name `xml:"ListBucketResult"`
		Name    string   `xml:"Name"`
	}
	body, err := readBucketBody(response.Body)
	if err != nil {
		return err
	}
	if xml.Unmarshal(body, &listed) != nil || listed.Name != a.Bucket {
		return errors.New("bucket listing did not match the selected bucket")
	}
	return nil
}

func probeGoogleBucket(ctx context.Context, client *http.Client, a BucketAccess) error {
	key, err := parseBucketServiceAccount(a.ServiceAccountJSON)
	if err != nil {
		return err
	}
	cfg := jwt.Config{Email: key.ClientEmail, PrivateKey: []byte(key.PrivateKey), PrivateKeyID: key.PrivateKeyID, Scopes: []string{"https://www.googleapis.com/auth/devstorage.read_only"}, TokenURL: key.TokenURI}
	token, err := cfg.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, client)).Token()
	if err != nil || token == nil || token.AccessToken == "" {
		return errors.New("the bucket application token could not be obtained")
	}
	endpoint := "https://storage.googleapis.com/storage/v1/b/" + url.PathEscape(a.Bucket) + "/o?maxResults=1&fields=kind"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("bucket listing was refused")
	}
	body, err := readBucketBody(response.Body)
	if err != nil {
		return err
	}
	var listed struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(body, &listed) != nil || listed.Kind != "storage#objects" {
		return errors.New("bucket listing response could not be verified")
	}
	return nil
}

func readBucketBody(body io.Reader) ([]byte, error) {
	const limit = 128 * 1024
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("bucket response exceeded the check limit")
	}
	return data, nil
}
