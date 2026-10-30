package artifacts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	_ "github.com/xraph/trove/drivers/s3driver" // registers the s3 scheme
)

// A DigitalOcean Spaces URL signs with us-east-1 and names the real region
// in the endpoint. Open must reach the endpoint, not AWS, and address the
// bucket in the path.
func TestOpenSpacesStyleURL(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dsn := "s3://KEY:SECRET@us-east-1/loomplane-artifacts?endpoint=" + srv.URL + "&path_style=true"
	s, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(t.Context()) }()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no request reached the endpoint")
	}
	for _, p := range seen {
		if !strings.HasPrefix(strings.SplitN(p, " ", 2)[1], "/loomplane-artifacts") {
			t.Errorf("request %q not addressed to the bucket path", p)
		}
	}
	if !strings.Contains(auth, "Credential=KEY/") || !strings.Contains(auth, "/us-east-1/s3/") {
		t.Errorf("authorization %q not signed by KEY for us-east-1", auth)
	}
}
