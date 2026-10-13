package manifest

import (
	"regexp"
	"strings"
)

// Endpoint is a service's public address on an owned server or cluster.
type Endpoint struct {
	HostPort     int    `json:"host_port,omitempty" yaml:"host_port,omitempty"`
	Hostname     string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	IngressClass string `json:"ingress_class,omitempty" yaml:"ingress_class,omitempty"`
	TLSSecret    string `json:"tls_secret,omitempty" yaml:"tls_secret,omitempty"`
}

var (
	connectionRE = regexp.MustCompile(`^conn_[0-7][0-9a-hjkmnp-tv-z]{25}$`)
	imageRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9.:/_-]*@sha256:[a-f0-9]{64}$`)
	hostnameRE   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$`)
	labelRE      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	probePathRE  = regexp.MustCompile(`^/[A-Za-z0-9/._~%+-]*$`)
)

func checkConnection(add func(string, string, ...any), path, value string) {
	if value != "" && !connectionRE.MatchString(value) {
		add(path, "%q isn't a connection ID", value)
	}
}

func checkInfrastructure(add func(string, string, ...any), path string, s Settings) {
	checkConnection(add, path+".target", s.Target)
	if s.HealthPath != "" && (len(s.HealthPath) > 512 || !probePathRE.MatchString(s.HealthPath) || strings.HasPrefix(s.HealthPath, "//")) {
		add(path+".health_path", "must be an HTTP path on this service, without a query or fragment")
	}
	if e := s.Endpoint; e != nil {
		if e.HostPort < 0 || e.HostPort > MaxPort {
			add(path+".endpoint.host_port", "must be between 1 and %d", MaxPort)
		}
		if e.Hostname != "" && !validHostname(e.Hostname) {
			add(path+".endpoint.hostname", "%q isn't a hostname", e.Hostname)
		}
		if e.IngressClass != "" && !validHostname(e.IngressClass) {
			add(path+".endpoint.ingress_class", "%q isn't an ingress class name", e.IngressClass)
		}
		if e.TLSSecret != "" && (len(e.TLSSecret) > 63 || !labelRE.MatchString(e.TLSSecret)) {
			add(path+".endpoint.tls_secret", "%q isn't a Secret name", e.TLSSecret)
		}
	}
	if b := s.Build; b != nil {
		if b.Context != "" && !isDir(b.Context) {
			add(path+".build.context", "%q isn't a directory in the repository; use . for its root", b.Context)
		}
		checkConnection(add, path+".build.builder", b.Builder)
		checkConnection(add, path+".build.registry", b.Registry)
		if b.Image != "" && (len(b.Image) > 512 || !imageRE.MatchString(b.Image)) {
			add(path+".build.image", "must be a container image pinned by a sha256 digest")
		}
		if b.Image != "" && (b.Builder != "" || b.Context != "") {
			add(path+".build", "a pinned image skips the source build; omit builder and context")
		}
	}
}

func validHostname(value string) bool {
	if len(value) > 253 || !hostnameRE.MatchString(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) > 63 {
			return false
		}
	}
	return true
}
