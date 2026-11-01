package deployments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/xraph/loomplane/internal/enginetls"
)

type engineHooks struct{ CatalogHooks }

func (h engineHooks) GenerateSecret(_ context.Context, request SecretGeneration) ([]byte, error) {
	if request.Secret.Generator != enginetls.Generator || (request.Kind != "opensearch" && request.Kind != "duckdb") {
		return nil, errors.New("unsupported engine secret generator")
	}
	var password string
	if request.Kind == "opensearch" {
		password = string(request.Secrets["password"])
		if password == "" {
			return nil, errors.New("the OpenSearch TLS generation requires its recorded password")
		}
	}
	return enginetls.Generate(enginetls.Identity{Org: request.OrgID, Environment: request.EnvironmentID, Resource: request.Resource, Kind: request.Kind}, password, time.Now())
}

func (h engineHooks) ValidateSecret(_ context.Context, request SecretGeneration, raw []byte) error {
	if request.Secret.Generator != enginetls.Generator {
		return errors.New("unsupported engine secret generator")
	}
	bundle, err := enginetls.Parse(raw, enginetls.Identity{Org: request.OrgID, Environment: request.EnvironmentID, Resource: request.Resource, Kind: request.Kind}, time.Now())
	if err != nil {
		return err
	}
	if request.Kind == "opensearch" {
		return bundle.CheckPassword(string(request.Secrets["password"]))
	}
	return nil
}

func (h engineHooks) Files(ctx context.Context, run ComponentRun) (map[string][]byte, error) {
	if run.Component.Readiness == nil || run.Component.Readiness.Checker == "" {
		return h.CatalogHooks.Files(ctx, run)
	}
	password := string(run.Secrets["password"])
	switch run.Kind {
	case "falkordb":
		if password == "" {
			return nil, errors.New("the FalkorDB password is missing")
		}
		return map[string][]byte{"/etc/loomplane/password": []byte(password), "/etc/loomplane/check-ready": []byte("set -eu\nREDISCLI_AUTH=$(cat /etc/loomplane/password)\nexport REDISCLI_AUTH\n[ \"$(redis-cli --no-auth-warning --raw ping 2>/dev/null)\" = PONG ]\n")}, nil
	case "elasticsearch":
		if password == "" {
			return nil, errors.New("the Elasticsearch password is missing")
		}
		return map[string][]byte{"/usr/share/elasticsearch/config/loomplane-probe.conf": probeConfig("http", "elastic", password, "")}, nil
	case "duckdb", "opensearch":
		bundle, err := enginetls.Parse(run.Secrets["tls"], runTLSIdentity(run), time.Now())
		if err != nil {
			return nil, err
		}
		if run.Kind == "duckdb" {
			token := run.Secrets["token"]
			if len(token) != 64 {
				return nil, errors.New("the DuckDB token is missing")
			}
			return map[string][]byte{"/etc/loomplane/token": token, "/etc/loomplane/ca.pem": bundle.CACert, "/etc/loomplane/node-key.pem": bundle.NodeKey}, nil
		}
		if checkErr := bundle.CheckPassword(password); checkErr != nil {
			return nil, checkErr
		}
		files := openSearchSecurityFiles(bundle.PasswordHash)
		files["/usr/share/opensearch/config/ca.pem"] = bundle.CACert
		files["/usr/share/opensearch/config/node-key.pem"] = bundle.NodeKey
		files["/usr/share/opensearch/config/loomplane-probe.conf"] = probeConfig("https", "loomplane", password, "/usr/share/opensearch/config/ca.pem")
		files["/usr/share/opensearch/config/opensearch.yml"] = []byte(fmt.Sprintf(openSearchConfig, bundle.NodeName()))
		return files, nil
	}
	return nil, errors.New("unsupported engine runtime hook")
}

func probeConfig(scheme, user, password, ca string) []byte {
	// Generated passwords are hex. Quoting also handles a recorded credential
	// without interpreting its content as curl configuration.
	value := fmt.Sprintf("url = %q\nuser = %q\nmax-time = 4\n", scheme+"://localhost:9200/_cluster/health?wait_for_status=yellow&timeout=1s", user+":"+password)
	if ca != "" {
		value += fmt.Sprintf("cacert = %q\n", ca)
	}
	return []byte(value)
}

func openSearchSecurityFiles(hash string) map[string][]byte {
	bodies := map[string]string{
		"action_groups": "", "nodes_dn": "", "tenants": "",
		"allowlist":      "config:\n  enabled: false\n",
		"audit":          "config:\n  enabled: true\n  audit:\n    exclude_sensitive_headers: true\n    log_request_body: false\n",
		"internal_users": fmt.Sprintf("loomplane:\n  hash: %q\n  reserved: true\n", hash),
		"roles":          "loomplane_admin:\n  cluster_permissions: [\"*\"]\n  index_permissions:\n    - index_patterns: [\"*\"]\n      allowed_actions: [\"*\"]\n",
		"roles_mapping":  "loomplane_admin:\n  reserved: true\n  users: [\"loomplane\"]\n",
		"config":         "config:\n  dynamic:\n    http:\n      anonymous_auth_enabled: false\n    authc:\n      basic_internal_auth_domain:\n        http_enabled: true\n        transport_enabled: true\n        order: 0\n        http_authenticator:\n          type: basic\n          challenge: true\n        authentication_backend:\n          type: intern\n",
	}
	files := map[string][]byte{}
	for name, body := range maps.All(bodies) {
		files["/usr/share/opensearch/config/opensearch-security/"+name+".yml"] = []byte(fmt.Sprintf("_meta:\n  type: %q\n  config_version: 2\n%s", name, body))
	}
	return files
}

const openSearchConfig = `cluster.name: loomplane
network.host: 0.0.0.0
discovery.type: single-node
node.store.allow_mmap: false
transport.host: 127.0.0.1
plugins.security.disabled: false
plugins.security.allow_default_init_securityindex: true
plugins.security.ssl.http.enabled: true
plugins.security.ssl.http.pemcert_filepath: node.pem
plugins.security.ssl.http.pemkey_filepath: node-key.pem
plugins.security.ssl.http.pemtrustedcas_filepath: ca.pem
plugins.security.ssl.transport.pemcert_filepath: node.pem
plugins.security.ssl.transport.pemkey_filepath: node-key.pem
plugins.security.ssl.transport.pemtrustedcas_filepath: ca.pem
plugins.security.ssl.transport.enforce_hostname_verification: true
plugins.security.nodes_dn: ["CN=%s"]
`
