package backupfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/xraph/trove"
)

type destination struct {
	Driver   string
	Location string
}

// New borrows a configured Trove store. Its destination identity describes the
// configured namespace, not a physical disk or cloud-account identity.
func New(files *trove.Trove, bucket, dsn string) (*Store, error) {
	locator, err := destinationLocator(dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{Trove: files, Bucket: bucket, destination: &locator}
	if checkErr := store.validate("destination-probe"); checkErr != nil {
		return nil, checkErr
	}
	if _, err = store.destinationID("destination-probe"); err != nil {
		return nil, err
	}
	return store, nil
}

func destinationLocator(dsn string) (destination, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return destination{}, errors.New("invalid backup destination configuration")
	}
	params, queryErr := url.ParseQuery(u.RawQuery)
	if queryErr != nil {
		return destination{}, errors.New("invalid backup destination query parameters")
	}
	switch u.Scheme {
	case "file", "local":
		root := u.Path
		if u.Scheme == "local" && u.Host != "" {
			root = u.Host + root
		}
		if root == "" {
			return destination{}, errors.New("backup destination requires a local directory")
		}
		absolute, absErr := filepath.Abs(root)
		if absErr != nil {
			return destination{}, errors.New("backup destination directory cannot be resolved")
		}
		return destination{Driver: "local", Location: filepath.Clean(absolute)}, nil
	case "s3":
		region := u.Host
		if region == "" {
			region = "us-east-1"
		}
		if strings.TrimPrefix(u.Path, "/") == "" {
			return destination{}, errors.New("backup destination requires an S3 default bucket")
		}
		endpoint := params.Get("endpoint")
		if endpoint != "" {
			parsed, parseErr := url.Parse(endpoint)
			if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return destination{}, errors.New("backup S3 endpoint requires an absolute HTTP URL without a query or fragment")
			}
			parsed.User = nil
			parsed.Host = strings.ToLower(parsed.Host)
			if parsed.Path == "/" {
				parsed.Path = ""
			}
			endpoint = parsed.String()
		}
		return destination{Driver: "s3", Location: region + "\n" + endpoint}, nil
	case "mem":
		return destination{Driver: "mem", Location: "memory"}, nil
	default:
		return destination{}, errors.New("backup destination driver is not supported")
	}
}

func (s *Store) destinationID(key string) (string, error) {
	if s.destination == nil {
		return "", errors.New("backup destination identity is not configured")
	}
	if s.Trove.DriverFor(s.Bucket, key).Name() != s.destination.Driver {
		return "", errors.New("backup driver differs from its configured destination")
	}
	raw, err := json.Marshal(struct {
		Version     int
		Destination destination
		Bucket      string
	}{1, *s.destination, s.Bucket})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) checkDestination(ref Object, retirement bool) error {
	if ref.DestinationID == "" && !retirement {
		// Legacy points remain recoverable through their recorded checksum.
		return nil
	}
	current, err := s.destinationID(ref.Key)
	if err != nil {
		return err
	}
	if ref.DestinationID == "" || current != ref.DestinationID {
		return errors.New("backup destination does not match the recorded recovery point")
	}
	return nil
}
