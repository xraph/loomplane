package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const resourceAccount = "023e105f4ecef8ad9ca31a8372d0c353"
const resourceHexID = "a23e105f4ecef8ad9ca31a8372d0c353"
const resourceD1ID = "b23e105f-4ece-48ad-9ca3-1a8372d0c353"

func resourceTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return New(s.URL, false)
}

func TestResourceInventoryUsesNativeEndpointsAndProjectsMetadata(t *testing.T) {
	tests := []struct {
		kind                    ResourceKind
		path, fixture, id, name string
	}{
		{ResourceD1, "/d1/database", `[{"uuid":"` + resourceD1ID + `","name":"db","created_at":"2026-01-01T00:00:00Z","jurisdiction":"eu","password":"hidden"}]`, resourceD1ID, "db"},
		{ResourceKV, "/storage/kv/namespaces", `[{"id":"` + resourceHexID + `","title":"cache","jurisdiction":"eu","secret":"hidden"}]`, resourceHexID, "cache"},
		{ResourceQueue, "/queues", `[{"queue_id":"` + resourceHexID + `","queue_name":"jobs","created_on":"2026-01-01T00:00:00Z","consumers":[{"secret":"hidden"}]}]`, resourceHexID, "jobs"},
		{ResourceVectorize, "/vectorize/v2/indexes", `[{"name":"embeddings","config":{"dimensions":768,"metric":"cosine"},"secret":"hidden"}]`, "embeddings", "embeddings"},
		{ResourceHyperdrive, "/hyperdrive/configs", `[{"id":"` + resourceHexID + `","name":"pool","origin":{"password":"hidden","host":"private","user":"user"}}]`, resourceHexID, "pool"},
		{ResourceDurableObject, "/workers/durable_objects/namespaces", `[{"id":"` + resourceHexID + `","name":"objects","class":"Counter","script":"worker","use_sqlite":true,"secret":"hidden"}]`, resourceHexID, "objects"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/accounts/"+resourceAccount+tt.path || r.Method != http.MethodGet {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing authentication")
				}
				fmt.Fprintf(w, `{"success":true,"result":%s,"result_info":{"page":1,"total_pages":1}}`, tt.fixture)
			})
			got, err := c.ListResources(context.Background(), "token", resourceAccount, tt.kind)
			if err != nil || len(got) != 1 {
				t.Fatalf("got %v, %v", got, err)
			}
			if got[0].ID != tt.id || got[0].Name != tt.name || got[0].Kind != tt.kind {
				t.Fatalf("wrong native resource: %#v", got[0])
			}
			data, _ := json.Marshal(got)
			if strings.Contains(string(data), "hidden") || strings.Contains(string(data), "private") {
				t.Fatalf("credential fields leaked: %s", data)
			}
			if tt.kind == ResourceVectorize && got[0].Metadata["dimensions"] != "768" {
				t.Fatalf("missing index dimensions: %#v", got)
			}
			if tt.kind == ResourceDurableObject && got[0].Metadata["class"] != "Counter" {
				t.Fatalf("missing binding metadata: %#v", got)
			}
		})
	}
}

func TestResourceInventoryPaginatesWithoutReturningPartialSuccess(t *testing.T) {
	calls := 0
	c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprintf(w, `{"success":true,"result":[{"id":"%s","title":"one"}],"result_info":{"page":1,"per_page":1,"total_count":2}}`, resourceHexID)
			return
		}
		if r.URL.Query().Get("page") != "2" {
			t.Errorf("wrong page %s", r.URL)
		}
		fmt.Fprintf(w, `{"success":true,"result":[{"id":"023e105f4ecef8ad9ca31a8372d0c354","title":"two"}],"result_info":{"page":2,"per_page":1,"total_count":2}}`)
	})
	got, err := c.ListResources(context.Background(), "token", resourceAccount, ResourceKV)
	if err != nil || len(got) != 2 || calls != 2 {
		t.Fatalf("got %v, %v; calls=%d", got, err, calls)
	}
	c = resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprintf(w, `{"success":true,"result":[{"id":"%s","title":"one"}],"result_info":{"page":1,"total_pages":2}}`, resourceHexID)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"hidden"}]}`)
	})
	got, err = c.ListResources(context.Background(), "token", resourceAccount, ResourceKV)
	if !errors.Is(err, ErrRejected) || got != nil || strings.Contains(err.Error(), "hidden") {
		t.Fatalf("partial inventory reported as complete: %v %v", got, err)
	}
}

func TestResourceR2PaginationAndJurisdiction(t *testing.T) {
	calls := 0
	c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("cf-r2-jurisdiction") != "eu" || r.URL.Path != "/accounts/"+resourceAccount+"/r2/buckets" {
			t.Errorf("wrong R2 scope %s %s", r.Header, r.URL)
		}
		if calls == 1 {
			fmt.Fprint(w, `{"success":true,"result":{"buckets":[{"name":"first-bucket","location":"weur"}]},"result_info":{"cursor":"next+token"}}`)
			return
		}
		if r.URL.Query().Get("cursor") != "next+token" {
			t.Errorf("wrong cursor: %s", r.URL)
		}
		fmt.Fprint(w, `{"success":true,"result":{"buckets":[{"name":"second-bucket"}]}}`)
	})
	got, err := c.ListResourcesInJurisdiction(context.Background(), "token", resourceAccount, ResourceR2, "eu")
	if err != nil || len(got) != 2 || got[0].ID != "first-bucket" || got[1].Metadata["jurisdiction"] != "eu" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestResourceCreateUsesProviderPayloadAndReturnsNativeIdentity(t *testing.T) {
	tests := []struct {
		opts                   CreateResourceOptions
		path, body, result, id string
	}{
		{CreateResourceOptions{Kind: ResourceD1, Name: "db", Location: "weur", Jurisdiction: "eu"}, "/d1/database", `{"name":"db","primary_location_hint":"weur","jurisdiction":"eu"}`, `{"uuid":"` + resourceD1ID + `","name":"db","created_at":"2026-01-01T00:00:00Z"}`, resourceD1ID},
		{CreateResourceOptions{Kind: ResourceKV, Name: "cache"}, "/storage/kv/namespaces", `{"title":"cache"}`, `{"id":"` + resourceHexID + `","title":"cache"}`, resourceHexID},
		{CreateResourceOptions{Kind: ResourceR2, Name: "assets", Location: "weur", Jurisdiction: "eu"}, "/r2/buckets", `{"name":"assets","locationHint":"weur"}`, `{"name":"assets"}`, "assets"},
		{CreateResourceOptions{Kind: ResourceQueue, Name: "jobs", Jurisdiction: "eu"}, "/queues", `{"queue_name":"jobs","jurisdiction":"eu"}`, `{"queue_id":"` + resourceHexID + `","queue_name":"jobs"}`, resourceHexID},
		{CreateResourceOptions{Kind: ResourceVectorize, Name: "embeddings", Dimensions: 768, Metric: "cosine"}, "/vectorize/v2/indexes", `{"name":"embeddings","config":{"dimensions":768,"metric":"cosine"}}`, `{"name":"embeddings","config":{"dimensions":768,"metric":"cosine"}}`, "embeddings"},
		{CreateResourceOptions{Kind: ResourceHyperdrive, Name: "pool", Origin: &HyperdriveOrigin{Scheme: "postgres", Host: "db.example.com", Port: 5432, Database: "app", User: "app", Password: "hidden"}}, "/hyperdrive/configs", `{"name":"pool","origin":{"scheme":"postgres","host":"db.example.com","port":5432,"database":"app","user":"app","password":"hidden"}}`, `{"id":"` + resourceHexID + `","name":"pool","origin":{"password":"hidden"}}`, resourceHexID},
	}
	for _, tt := range tests {
		t.Run(string(tt.opts.Kind), func(t *testing.T) {
			c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/accounts/"+resourceAccount+tt.path {
					t.Errorf("wrong create endpoint: %s %s", r.Method, r.URL)
				}
				var got, want any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				if err := json.Unmarshal([]byte(tt.body), &want); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("wrong provider body: %#v want %#v", got, want)
				}
				if tt.opts.Kind == ResourceR2 && r.Header.Get("cf-r2-jurisdiction") != "eu" {
					t.Error("R2 jurisdiction missing")
				}
				fmt.Fprintf(w, `{"success":true,"result":%s}`, tt.result)
			})
			got, err := c.CreateResource(context.Background(), "token", resourceAccount, tt.opts)
			if err != nil || got.ID != tt.id || got.Name != tt.opts.Name {
				t.Fatalf("got %#v %v", got, err)
			}
			data, _ := json.Marshal(got)
			if strings.Contains(string(data), "hidden") {
				t.Fatalf("secret leaked: %s", data)
			}
		})
	}
}

func TestResourceGetDeleteAndDurableObjectLifecycle(t *testing.T) {
	tests := []struct {
		kind             ResourceKind
		id, path, result string
	}{
		{ResourceD1, resourceD1ID, "/d1/database/" + resourceD1ID, `{"uuid":"` + resourceD1ID + `","name":"db"}`},
		{ResourceKV, resourceHexID, "/storage/kv/namespaces/" + resourceHexID, `{"id":"` + resourceHexID + `","title":"cache"}`},
		{ResourceR2, "assets", "/r2/buckets/assets", `{"name":"assets","jurisdiction":"eu"}`},
		{ResourceQueue, resourceHexID, "/queues/" + resourceHexID, `{"queue_id":"` + resourceHexID + `","queue_name":"jobs"}`},
		{ResourceVectorize, "index-one", "/vectorize/v2/indexes/index-one", `{"name":"index-one"}`},
		{ResourceHyperdrive, resourceHexID, "/hyperdrive/configs/" + resourceHexID, `{"id":"` + resourceHexID + `","name":"pool"}`},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			methods := []string{}
			c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				if r.URL.Path != "/accounts/"+resourceAccount+tt.path {
					t.Errorf("wrong endpoint %s", r.URL)
				}
				if tt.kind == ResourceR2 && r.Header.Get("cf-r2-jurisdiction") != "eu" {
					t.Error("wrong R2 scope")
				}
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				fmt.Fprintf(w, `{"success":true,"result":%s}`, tt.result)
			})
			jurisdiction := ""
			if tt.kind == ResourceR2 {
				jurisdiction = "eu"
			}
			got, err := c.GetResourceInJurisdiction(context.Background(), "token", resourceAccount, tt.kind, tt.id, jurisdiction)
			if err != nil || got.ID != tt.id {
				t.Fatalf("got %#v %v", got, err)
			}
			if err = c.DeleteResourceInJurisdiction(context.Background(), "token", resourceAccount, tt.kind, tt.id, jurisdiction); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(methods, []string{"GET", "DELETE"}) {
				t.Fatalf("wrong methods %v", methods)
			}
		})
	}
	c := resourceTestClient(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("unexpected standalone durable object mutation") })
	if _, err := c.CreateResource(context.Background(), "token", resourceAccount, CreateResourceOptions{Kind: ResourceDurableObject, Name: "objects"}); !errors.Is(err, ErrResourceOperationUnsupported) {
		t.Fatalf("got %v", err)
	}
	if err := c.DeleteResource(context.Background(), "token", resourceAccount, ResourceDurableObject, resourceHexID); !errors.Is(err, ErrResourceOperationUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestResourceRejectsInvalidIdentityAndMalformedResponses(t *testing.T) {
	c := resourceTestClient(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("invalid identity sent to provider") })
	for _, account := range []string{"", "../other", "bad/account", strings.Repeat("a", 33)} {
		if _, err := c.ListResources(context.Background(), "token", account, ResourceKV); err == nil {
			t.Errorf("accepted account %q", account)
		}
	}
	for _, id := range []string{"", "../other", "one/two", "one%2ftwo", "?query"} {
		if _, err := c.GetResource(context.Background(), "token", resourceAccount, ResourceVectorize, id); err == nil {
			t.Errorf("accepted ID %q", id)
		}
	}
	for _, body := range []string{`not-json`, `{"success":true}`, `{"success":true,"result":[{"title":"no-id"}]}`, `{"success":true,"result":null}`} {
		t.Run(body, func(t *testing.T) {
			malformedClient := resourceTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			if got, err := malformedClient.ListResources(context.Background(), "token", resourceAccount, ResourceKV); err == nil || got != nil {
				t.Fatalf("malformed inventory accepted: %v %v", got, err)
			}
		})
	}
	c = resourceTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"result":{"id":"different","title":"cache"}}`)
	})
	if _, err := c.GetResource(context.Background(), "token", resourceAccount, ResourceKV, resourceHexID); err == nil {
		t.Error("accepted mismatched resource identity")
	}
}

func TestResourceCreateSupportsUSJurisdiction(t *testing.T) {
	for _, kind := range []ResourceKind{ResourceD1, ResourceKV} {
		t.Run(string(kind), func(t *testing.T) {
			c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["jurisdiction"] != "us" || r.Header.Get("cf-r2-jurisdiction") != "" {
					t.Errorf("wrong jurisdiction contract: %v %v", body, r.Header)
				}
				if kind == ResourceD1 {
					fmt.Fprintf(w, `{"success":true,"result":{"uuid":"%s","name":"app","jurisdiction":"us"}}`, resourceD1ID)
				} else {
					fmt.Fprintf(w, `{"success":true,"result":{"id":"%s","title":"app","jurisdiction":"us"}}`, resourceHexID)
				}
			})
			got, err := c.CreateResource(context.Background(), "token", resourceAccount, CreateResourceOptions{Kind: kind, Name: "app", Jurisdiction: "us"})
			if err != nil || got.Metadata["jurisdiction"] != "us" {
				t.Fatalf("got %#v %v", got, err)
			}
		})
	}
}

func TestResourceRejectsUnsupportedCreateConfiguration(t *testing.T) {
	c := resourceTestClient(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("invalid create sent to provider") })
	for _, opts := range []CreateResourceOptions{
		{Kind: ResourceVectorize, Name: "index", Dimensions: 0, Metric: "cosine"},
		{Kind: ResourceVectorize, Name: "index", Dimensions: 1537, Metric: "cosine"},
		{Kind: ResourceVectorize, Name: "index", Dimensions: 768, Metric: "bad"},
		{Kind: ResourceR2, Name: "UPPERCASE"},
		{Kind: ResourceR2, Name: "bucket", Jurisdiction: "invalid"},
		{Kind: ResourceD1, Name: "db", Location: "invalid"},
		{Kind: ResourceKV, Name: "cache", Jurisdiction: "invalid"},
		{Kind: ResourceQueue, Name: "jobs", Jurisdiction: "invalid"},
		{Kind: ResourceHyperdrive, Name: "pool"},
		{Kind: ResourceKV, Name: "cache", Location: "weur"},
		{Kind: ResourceKV, Name: "cache", Dimensions: 768},
		{Kind: ResourceKV, Name: "cache", Metric: "cosine"},
		{Kind: ResourceKV, Name: "cache", Origin: &HyperdriveOrigin{Password: "hidden"}},
		{Kind: ResourceVectorize, Name: "index", Dimensions: 768, Metric: "cosine", Jurisdiction: "eu"},
		{Kind: ResourceHyperdrive, Name: "pool", Origin: &HyperdriveOrigin{Scheme: "postgres", Host: "host/path", Database: "app", User: "app", Password: "hidden"}},
	} {
		if _, err := c.CreateResource(context.Background(), "token", resourceAccount, opts); !errors.Is(err, ErrInvalidResource) {
			t.Errorf("accepted invalid %s config: %v", opts.Kind, err)
		}
	}
	for _, kind := range []ResourceKind{ResourceD1, ResourceVectorize, ResourceHyperdrive} {
		if _, err := c.ListResourcesInJurisdiction(context.Background(), "token", resourceAccount, kind, "eu"); !errors.Is(err, ErrInvalidResource) {
			t.Errorf("accepted unsupported list scope for %s", kind)
		}
	}
}

func TestResourceInventoryRejectsStalledPaginationAndCrossJurisdiction(t *testing.T) {
	for _, fixture := range []string{
		`{"success":true,"result":{"buckets":[{"name":"bucket-one"}]},"result_info":{"cursor":"same"}}`,
		`{"success":true,"result":{"buckets":[{"name":"bucket-one","jurisdiction":"us"}]}}`,
	} {
		c := resourceTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, fixture) })
		if got, err := c.ListResourcesInJurisdiction(context.Background(), "token", resourceAccount, ResourceR2, "eu"); err == nil || got != nil {
			t.Fatalf("accepted incomplete R2 inventory %v %v", got, err)
		}
	}
	c := resourceTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"success":true,"result":[{"id":"%s","title":"cache"}],"result_info":{"page":1,"total_pages":2}}`, resourceHexID)
	})
	if got, err := c.ListResources(context.Background(), "token", resourceAccount, ResourceKV); err == nil || got != nil {
		t.Fatalf("accepted stalled page %v %v", got, err)
	}
}

func TestResourceDurableObjectGetUsesMetadataInventory(t *testing.T) {
	c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, resourceHexID) {
			t.Error("invented durable object namespace get endpoint")
		}
		fmt.Fprintf(w, `{"success":true,"result":[{"id":"%s","name":"objects","class":"Counter","script":"counter-worker","use_sqlite":true}]}`, resourceHexID)
	})
	got, err := c.GetResource(context.Background(), "token", resourceAccount, ResourceDurableObject, resourceHexID)
	if err != nil || got.Metadata["script"] != "counter-worker" || got.Metadata["use_sqlite"] != "true" {
		t.Fatalf("got %#v %v", got, err)
	}
	_, err = c.GetResource(context.Background(), "token", resourceAccount, ResourceDurableObject, resourceAccount)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("missing namespace: %v", err)
	}
}

func TestResourceMutationErrorsDoNotLeakCredentials(t *testing.T) {
	c := resourceTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"hidden credentials"}]}`)
	})
	_, err := c.CreateResource(context.Background(), "token", resourceAccount, CreateResourceOptions{Kind: ResourceHyperdrive, Name: "pool", Origin: &HyperdriveOrigin{Scheme: "postgres", Host: "db.example.com", Database: "app", User: "app", Password: "hidden"}})
	if err == nil || strings.Contains(err.Error(), "hidden") {
		t.Fatalf("unsafe create error %v", err)
	}
	err = c.DeleteResource(context.Background(), "token", resourceAccount, ResourceKV, resourceHexID)
	if err == nil || strings.Contains(err.Error(), "hidden") {
		t.Fatalf("unsafe delete error %v", err)
	}
}

func TestResourceDurableObjectInventoryKeepsNamespacesWithoutDisplayName(t *testing.T) {
	c := resourceTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"success":true,"result":[{"id":%q,"class":"Counter","script":"owning-worker","use_sqlite":true}]}`, resourceHexID)
	})
	got, err := c.ListResources(context.Background(), "token", resourceAccount, ResourceDurableObject)
	if err != nil || len(got) != 1 || got[0].Name != "Counter" {
		t.Fatalf("namespace %#v %v", got, err)
	}
}

func TestResourceHyperdriveCreatePreservesVerifiedOriginTLS(t *testing.T) {
	c := resourceTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mtls, ok := body["mtls"].(map[string]any)
		if !ok || mtls["ca_certificate_id"] != resourceD1ID || mtls["sslmode"] != "verify-full" {
			t.Fatalf("origin TLS weakened: %#v", body)
		}
		fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"name":"pool"}}`, resourceHexID)
	})
	_, err := c.CreateResource(context.Background(), "token", resourceAccount, CreateResourceOptions{Kind: ResourceHyperdrive, Name: "pool", Origin: &HyperdriveOrigin{Scheme: "postgres", Host: "db.example.com", Database: "app", User: "app", Password: "secret"}, CACertificateID: resourceD1ID, SSLMode: "verify-full"})
	if err != nil {
		t.Fatal(err)
	}
}
