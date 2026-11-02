package vercel

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

// ParseProvisioningMetadata validates flat, non-secret Marketplace options.
// The returned map can be encoded for both plan review and provider requests.
func ParseProvisioningMetadata(raw string) (map[string]any, error) {
	invalid := func() (map[string]any, error) {
		return nil, errors.New("vercel provisioning metadata must be a non-secret flat JSON object of at most 8 KiB")
	}
	if len(raw) > 8<<10 {
		return invalid()
	}
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return invalid()
	}
	out := map[string]any{}
	for d.More() {
		token, tokenErr := d.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok || !resourceIdentifier.MatchString(key) || len(key) > 128 || sensitiveMetadataName(key) {
			return invalid()
		}
		if _, duplicate := out[key]; duplicate {
			return invalid()
		}
		var value any
		if decodeErr := d.Decode(&value); decodeErr != nil || !validMetadataValue(value) {
			return invalid()
		}
		out[key] = value
	}
	closing, closeErr := d.Token()
	if closeErr != nil || closing != json.Delim('}') {
		return invalid()
	}
	var trailing any
	if trailingErr := d.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
		return invalid()
	}
	return out, nil
}

func sensitiveMetadataName(key string) bool {
	normal := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	for _, word := range []string{"password", "secret", "token", "credential", "connection", "apikey", "accesskey", "privatekey", "authorization"} {
		if strings.Contains(normal, word) {
			return true
		}
	}
	return false
}

func validMetadataValue(value any) bool {
	if array, ok := value.([]any); ok {
		for _, item := range array {
			if !validMetadataScalar(item) {
				return false
			}
		}
		return true
	}
	return validMetadataScalar(value)
}

func validMetadataScalar(value any) bool {
	switch typed := value.(type) {
	case bool, json.Number:
		return true
	case string:
		if strings.ContainsAny(typed, "\x00\r\n") {
			return false
		}
		parsed, err := url.Parse(typed)
		if err != nil {
			return false
		}
		if parsed.User != nil || parsed.Fragment != "" {
			return false
		}
		if parsed.Opaque != "" {
			opaque, decodeErr := url.PathUnescape(parsed.Opaque)
			if decodeErr != nil || strings.Contains(opaque, "@") {
				return false
			}
		}
		query, queryErr := url.ParseQuery(parsed.RawQuery)
		if queryErr != nil {
			return false
		}
		for key := range query {
			if sensitiveMetadataName(key) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
