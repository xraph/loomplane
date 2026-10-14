package kubernetes

import (
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

func kubeconfig(t *testing.T, endpoint string, ca []byte) []byte {
	t.Helper()
	c := api.Config{CurrentContext: "test",
		Clusters:  map[string]*api.Cluster{"test": {Server: endpoint, CertificateAuthorityData: ca}},
		AuthInfos: map[string]*api.AuthInfo{"test": {Token: "test-token"}},
		Contexts:  map[string]*api.Context{"test": {Cluster: "test", AuthInfo: "test", Namespace: "apps"}},
	}
	out, err := clientcmd.Write(c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testCA(t *testing.T) []byte {
	t.Helper()
	s := httptest.NewTLSServer(nil)
	t.Cleanup(s.Close)
	der, err := x509.MarshalPKIXPublicKey(s.Certificate().PublicKey)
	if err != nil || len(der) == 0 {
		t.Fatal("test certificate has no public key")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
}

func TestConfig_isExplicitAndSelfContained(t *testing.T) {
	raw := kubeconfig(t, "https://cluster.example.com:6443", testCA(t))
	c, err := ParseConfig(raw, map[string]string{"namespace": "preview", "image_pull_secret": "existing-pull"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Details["image_pull_secret"] != "existing-pull" || c.Namespace != "preview" || c.REST.Host != "https://cluster.example.com:6443" || c.REST.BearerToken != "test-token" {
		t.Fatalf("wrong explicit configuration: %+v", c.Details)
	}
	for _, key := range []string{"token", "client_key", "credential"} {
		if c.Details[key] != "" {
			t.Fatal("credential escaped into details")
		}
	}
}

func TestConfig_rejectsExecutableAndAmbientCredentials(t *testing.T) {
	base := kubeconfig(t, "https://cluster.example.com", testCA(t))
	for _, tc := range []struct {
		name, want string
		change     func(*api.Config)
	}{
		{"exec", "exec", func(c *api.Config) {
			c.AuthInfos["test"].Exec = &api.ExecConfig{Command: "curl", APIVersion: "client.authentication.k8s.io/v1"}
		}},
		{"auth provider", "auth-provider", func(c *api.Config) { c.AuthInfos["test"].AuthProvider = &api.AuthProviderConfig{Name: "gcp"} }},
		{"token file", "file", func(c *api.Config) { c.AuthInfos["test"].TokenFile = "/tmp/token" }},
		{"client file", "file", func(c *api.Config) { c.AuthInfos["test"].ClientKey = "/tmp/key" }},
		{"CA file", "file", func(c *api.Config) { c.Clusters["test"].CertificateAuthority = "/tmp/ca" }},
		{"insecure", "certificate", func(c *api.Config) { c.Clusters["test"].InsecureSkipTLSVerify = true }},
		{"HTTP", "https", func(c *api.Config) { c.Clusters["test"].Server = "http://cluster.example.com" }},
		{"userinfo", "endpoint", func(c *api.Config) { c.Clusters["test"].Server = "https://user:password@cluster.example.com" }},
		{"proxy", "proxy", func(c *api.Config) { c.Clusters["test"].ProxyURL = "https://proxy.example.com" }},
		{"missing token", "token", func(c *api.Config) { c.AuthInfos["test"].Token = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := clientcmd.Load(base)
			if err != nil {
				t.Fatal(err)
			}
			tc.change(c)
			raw, err := clientcmd.Write(*c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ParseConfig(raw, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe config: %v, want %s", err, tc.want)
			}
		})
	}
}

func TestConfig_rejectsReservedOrInvalidPullSecretNames(t *testing.T) {
	raw := kubeconfig(t, "https://cluster.example.com", testCA(t))
	for _, name := range []string{"../foreign", "lp-inst-owned-pull", "UPPER"} {
		if _, err := ParseConfig(raw, map[string]string{"image_pull_secret": name}); err == nil {
			t.Fatalf("accepted pull Secret %s", name)
		}
	}
}
