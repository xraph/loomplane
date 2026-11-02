package fleet

import (
	"regexp"
	"slices"
	"strings"
)

// ConfigValue binds one operator-declared environment key to a typed input.
// There is no expression language, manifest-path substitution or command expansion.
type ConfigValue struct {
	Parameter string  `json:"parameter,omitempty" yaml:"parameter,omitempty"`
	Secret    string  `json:"secret,omitempty" yaml:"secret,omitempty"`
	Literal   *string `json:"literal,omitempty" yaml:"literal,omitempty"`
}

var configKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

// ReservedConfigKey protects platform identity and executable lookup settings.
func ReservedConfigKey(key string) bool {
	return strings.HasPrefix(key, "LOOMPLANE_") || strings.HasPrefix(key, "KUBERNETES_") || slices.Contains([]string{"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PYTHONPATH", "KUBECONFIG"}, key)
}
func (t *Template) validateConfig() error {
	if len(t.Config) > len(t.Manifest.Services) {
		return ErrTemplate
	}
	for service, values := range t.Config {
		if _, ok := t.Manifest.Services[service]; !ok || len(values) > 128 {
			return ErrTemplate
		}
		for key, source := range values {
			if !configKey.MatchString(key) || ReservedConfigKey(key) {
				return ErrTemplate
			}
			count := 0
			if source.Parameter != "" {
				count++
				if _, ok := t.Parameters[source.Parameter]; !ok {
					return ErrTemplate
				}
			}
			if source.Secret != "" {
				count++
				s, ok := t.Secrets[source.Secret]
				if !ok || !slices.Contains(s.Services, service) {
					return ErrTemplate
				}
			}
			if source.Literal != nil {
				count++
				if len(*source.Literal) > 65536 || strings.ContainsRune(*source.Literal, 0) {
					return ErrTemplate
				}
			}
			if count != 1 {
				return ErrTemplate
			}
		}
	}
	return nil
}
