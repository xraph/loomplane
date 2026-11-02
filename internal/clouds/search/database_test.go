package search

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeSearchRejectsMalformedHTTPStatus(t *testing.T) {
	d := Database{Engine: "elasticsearch", Run: func(_ context.Context, _ []string, out io.Writer) error {
		_, err := io.WriteString(out, "{}\n20x")
		return err
	}}
	if _, err := d.api(t.Context(), "GET", "/", nil); err == nil {
		t.Fatal("malformed status was accepted")
	}
}

func TestSnapshotRejectsTraversalLinksAndCredentialFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  byte
		valid bool
	}{{"./index-0", tar.TypeReg, true}, {"indices/a/0/snap-a.dat", tar.TypeReg, true}, {"../index-0", tar.TypeReg, false}, {"indices/../../etc/passwd", tar.TypeReg, false}, {"ca.pem", tar.TypeReg, false}, {"indices/a/0/link", tar.TypeSymlink, false}, {"/index-0", tar.TypeReg, false}} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "archive"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			tw := tar.NewWriter(file)
			if err = tw.WriteHeader(&tar.Header{Name: tc.name, Typeflag: tc.kind, Linkname: "/etc/passwd"}); err != nil {
				t.Fatal(err)
			}
			if err = tw.Close(); err != nil {
				t.Fatal(err)
			}
			if got := ValidateSnapshot(file); (got == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, got)
			}
		})
	}
}

func TestNativeSnapshotIncludesHiddenUserDataAndStreamsWithoutSecurityState(t *testing.T) {
	for _, engine := range []string{"elasticsearch", "opensearch"} {
		t.Run(engine, func(t *testing.T) {
			cancelled, cancel := context.WithCancel(t.Context())
			defer cancel()
			var snapshot map[string]any
			cleaned := false
			db := Database{Engine: engine, Run: func(ctx context.Context, args []string, out io.Writer) error {
				if args[0] == "mkdir" {
					return nil
				}
				if args[0] == "rm" {
					if ctx.Err() != nil {
						t.Fatal("cleanup inherited cancelled context")
					}
					cleaned = true
					return nil
				}
				endpoint := ""
				method := ""
				var body []byte
				for i, arg := range args {
					if arg == "--url" {
						endpoint = args[i+1]
					}
					if arg == "--request" {
						method = args[i+1]
					}
					if arg == "--data-binary" {
						body = []byte(args[i+1])
					}
				}
				response := `{"acknowledged":true}`
				switch {
				case strings.Contains(endpoint, "/_resolve/index/"):
					response = `{"indices":[{"name":".security-7","attributes":["system","hidden"]},{"name":"events","attributes":["open"]},{"name":".user-history","attributes":["open","hidden"]},{"name":".ds-audit-1","attributes":["open","hidden"]}]}`
				case strings.Contains(endpoint, "/_cat/indices"):
					response = `[{"index":"events","system":"false"},{"index":".user-history","system":"false"},{"index":".ds-audit-1","system":"false"}]`
				case strings.Contains(endpoint, "/_data_stream/"):
					response = `{"data_streams":[{"name":"audit","indices":[{"index_name":".ds-audit-1"}]}]}`
				case strings.Contains(endpoint, "/backup?wait_for_completion"):
					if err := json.Unmarshal(body, &snapshot); err != nil {
						t.Fatal(err)
					}
					response = `{"snapshot":{"state":"SUCCESS"}}`
				case method == "DELETE":
					if ctx.Err() != nil {
						t.Fatal("snapshot cancellation inherited request cancellation")
					}
				}
				_, err := io.WriteString(out, response+"\n200")
				return err
			}, Download: func(context.Context, string, io.Writer) error { cancel(); return context.Canceled }}
			if err := db.Export(cancelled, io.Discard); !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if !cleaned {
				t.Fatal("failed snapshot left temporary repository files")
			}
			if snapshot["indices"] != ".user-history,audit,events" || snapshot["include_global_state"] != false {
				t.Fatalf("unsafe snapshot scope: %v", snapshot)
			}
			if engine == "elasticsearch" {
				feature, ok := snapshot["feature_states"].([]any)
				if !ok || len(feature) != 1 || feature[0] != "none" {
					t.Fatal("security feature state not excluded")
				}
			}
		})
	}
}
