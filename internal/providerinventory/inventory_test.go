package providerinventory

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure"
	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/clouds/render"

	"github.com/xraph/loomplane/internal/clouds/aws"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/connections"
)

func TestDigitalOceanInventoryVerifiesAccountBeforeResources(t *testing.T) {
	for _, match := range []bool{true, false} {
		t.Run(map[bool]string{true: "same-account", false: "substituted-account"}[match], func(t *testing.T) {
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/account":
					id := "account-1"
					if !match {
						id = "account-2"
					}
					w.Write([]byte(`{"account":{"uuid":"` + id + `"}}`))
				case "/v2/databases":
					reads++
					w.Write([]byte(`{"databases":[]}`))
				default:
					t.Errorf("unexpected %s", r.URL)
				}
			}))
			defer srv.Close()
			c := &connections.Connection{Cloud: "digitalocean", Details: map[string]string{"account_uuid": "account-1"}}
			got, err := (&Clients{DigitalOcean: digitalocean.New(srv.URL)}).Inventory(t.Context(), c, []byte("token"))
			if match {
				if err != nil || !got.Complete || !strings.Contains(got.Scope, "account-1") {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil || got.Complete || reads != 0 {
				t.Fatalf("substitution admitted: %+v %v reads=%d", got, err, reads)
			}
		})
	}
}
func TestAWSInventoryRejectsSubstitutedAccountBeforeListing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		if r.Form.Get("Action") != "GetCallerIdentity" {
			t.Error("listed before verifying identity")
		}
		w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult><Account>999999999999</Account><Arn>arn:aws:iam::999999999999:user/test</Arn></GetCallerIdentityResult></GetCallerIdentityResponse>`))
	}))
	defer srv.Close()
	got, err := (&Clients{AWS: aws.New(srv.URL)}).Inventory(t.Context(), &connections.Connection{Cloud: "aws", Account: "123456789012", Details: map[string]string{"region": "us-east-1"}}, []byte(`{"access_key_id":"AKIA1234567890123456","secret_access_key":"verylongsecretaccesskey"}`))
	if err == nil || got.Complete || calls != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
}
func TestInventoryInvalidAndUnsupportedNeverRead(t *testing.T) {
	c := &Clients{}
	for _, cloud := range []string{"digitalocean", "aws", "azure", "gcp", "render", "server", ""} {
		got, err := c.Inventory(t.Context(), &connections.Connection{Cloud: cloud}, []byte("SECRET"))
		if err == nil || got.Complete || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s %+v %v", cloud, got, err)
		}
	}
	for _, cloud := range []string{"digitalocean", "aws", "azure", "gcp", "render"} {
		if !c.Supports(cloud) {
			t.Errorf("missing %s", cloud)
		}
	}
	if c.Supports("server") {
		t.Error("unsupported advertised")
	}
}

func TestSupportedProvidersReflectsInstalledClients(t *testing.T) {
	c := &Clients{DigitalOcean: digitalocean.New("")}
	got := c.SupportedProviders()
	if len(got) != 1 || got[0] != "digitalocean" {
		t.Fatalf("installed providers = %v", got)
	}
}

func TestDigitalOceanInventoryReadsOAuthGrantAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("grant sent instead of access token: %q", r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v2/account" {
			w.Write([]byte(`{"account":{"uuid":"account-1"}}`))
			return
		}
		w.Write([]byte(`{"databases":[]}`))
	}))
	defer srv.Close()
	got, err := (&Clients{DigitalOcean: digitalocean.New(srv.URL)}).Inventory(t.Context(), &connections.Connection{Cloud: "digitalocean", Details: map[string]string{"account_uuid": "account-1"}}, []byte(`{"access_token":"access-token","refresh_token":"SECRET"}`))
	if err != nil || !got.Complete {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAzureInventoryUsesRecordedSubscriptionAndResourceGroup(t *testing.T) {
	const tenant = "11111111-1111-1111-1111-111111111111"
	const subscription = "22222222-2222-2222-2222-222222222222"
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			listed := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/token") {
					w.Write([]byte(`{"access_token":"token","expires_in":3600}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing authentication")
				}
				if r.URL.Path == "/subscriptions/"+subscription {
					id := subscription
					if wrong {
						id = tenant
					}
					w.Write([]byte(`{"subscriptionId":"` + id + `","tenantId":"` + tenant + `","state":"Enabled"}`))
					return
				}
				listed++
				if !strings.HasPrefix(r.URL.Path, "/subscriptions/"+subscription+"/resourceGroups/selected/") || r.Method != "GET" {
					t.Errorf("outside group: %s %s", r.Method, r.URL)
				}
				w.Write([]byte(`{"value":[]}`))
			}))
			defer srv.Close()
			conn := &connections.Connection{Cloud: "azure", Details: map[string]string{"tenant_id": tenant, "subscription_id": subscription, "client_id": "33333333-3333-3333-3333-333333333333", "resource_group": "selected"}}
			got, err := (&Clients{Azure: azure.New(srv.URL)}).Inventory(t.Context(), conn, []byte("secret"))
			if wrong {
				if err == nil || got.Complete || listed != 0 {
					t.Fatalf("%+v %v listed=%d", got, err, listed)
				}
			} else if err != nil || !got.Complete || listed != 3 {
				t.Fatalf("%+v %v listed=%d", got, err, listed)
			}
		})
	}
}
func TestRenderInventoryVerifiesSelectedOwner(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			listed := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing authentication")
				}
				if r.URL.Path == "/owners" {
					id := "tea-1"
					if wrong {
						id = "tea-2"
					}
					w.Write([]byte(`[{"owner":{"id":"` + id + `"}}]`))
					return
				}
				listed++
				if r.Method != "GET" || r.URL.Query().Get("ownerId") != "tea-1" {
					t.Error("outside owner")
				}
				w.Write([]byte(`[]`))
			}))
			defer srv.Close()
			got, err := (&Clients{Render: render.New(srv.URL)}).Inventory(t.Context(), &connections.Connection{Cloud: "render", Details: map[string]string{"owner_id": "tea-1"}}, []byte("token"))
			if wrong {
				if err == nil || got.Complete || listed != 0 {
					t.Fatalf("%+v %v listed=%d", got, err, listed)
				}
			} else if err != nil || !got.Complete || listed != 2 {
				t.Fatalf("%+v %v listed=%d", got, err, listed)
			}
		})
	}
}
func TestGoogleInventoryAuthenticatesAndPinsProject(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for _, scenario := range []string{"valid", "substituted-project", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":3600}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing authentication")
				}
				if r.Method != "GET" {
					t.Error("unexpected mutation")
				}
				if scenario == "expired" {
					w.WriteHeader(401)
					w.Write([]byte("SECRET"))
					return
				}
				if r.URL.Path == "/storage/v1/b" && r.URL.Query().Get("project") != "project-test" {
					t.Error("missing project filter")
				}
				w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			project := "project-test"
			if scenario == "substituted-project" {
				project = "project-other"
			}
			credential, e := json.Marshal(map[string]string{"type": "service_account", "project_id": project, "client_email": "account@project-test.iam.gserviceaccount.com", "private_key": string(private), "token_uri": srv.URL + "/token"})
			if e != nil {
				t.Fatal(e)
			}
			got, e := (&Clients{Google: gcp.New(srv.URL)}).Inventory(t.Context(), &connections.Connection{Cloud: "gcp", Details: map[string]string{"project_id": "project-test"}}, credential)
			switch scenario {
			case "valid":
				if e != nil || !got.Complete || calls != 4 {
					t.Fatalf("%+v %v calls=%d", got, e, calls)
				}
			case "substituted-project":
				if e == nil || got.Complete || calls != 0 {
					t.Fatalf("%+v %v calls=%d", got, e, calls)
				}
			default:
				if e == nil || got.Complete || strings.Contains(e.Error(), "SECRET") {
					t.Fatalf("%+v %v", got, e)
				}
			}
		})
	}
}
