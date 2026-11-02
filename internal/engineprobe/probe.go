// Package engineprobe names fixed authenticated checks for catalog engines.
package engineprobe

import "errors"

// Command returns a fixed checker invocation. Credentials are read from
// mounted files; callers cannot supply arguments or secret values.
func Command(kind, checker string) ([]string, error) {
	if checker != kind+"-v1" {
		return nil, errors.New("unsupported catalog readiness checker")
	}
	switch kind {
	case "falkordb":
		return []string{"/bin/sh", "/etc/loomplane/check-ready"}, nil
	case "elasticsearch":
		return []string{"curl", "--fail", "--silent", "--config", "/usr/share/elasticsearch/config/loomplane-probe.conf", "--output", "/dev/null"}, nil
	case "opensearch":
		return []string{"curl", "--fail", "--silent", "--config", "/usr/share/opensearch/config/loomplane-probe.conf", "--output", "/dev/null"}, nil
	case "duckdb":
		return []string{"/loomplane-duckdb", "check"}, nil
	}
	return nil, errors.New("unsupported catalog readiness checker")
}

// CertificatePath is the only deferred certificate destination for an engine.
func CertificatePath(kind string) (string, error) {
	switch kind {
	case "opensearch":
		return "/usr/share/opensearch/config/node.pem", nil
	case "duckdb":
		return "/etc/loomplane/node.pem", nil
	}
	return "", errors.New("engine does not declare deferred TLS")
}
