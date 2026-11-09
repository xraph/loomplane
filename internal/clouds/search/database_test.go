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

	"github.com/xraph/loomplane/internal/clouds/enginebackup"
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

func TestSnapshotAdmitsOnlyNativeShardPathMetadata(t *testing.T) {
	metadata := "snapshot_shard_paths/snapshot_path_a.events.1.0.1"
	for _, tc := range []struct {
		name    string
		headers []tar.Header
		valid   bool
	}{
		{"directory", []tar.Header{{Name: "snapshot_shard_paths/", Typeflag: tar.TypeDir}}, true},
		{"metadata", []tar.Header{{Name: metadata, Typeflag: tar.TypeReg}}, true},
		{"hidden index", []tar.Header{{Name: "snapshot_shard_paths/snapshot_path_a..user-history.1.0.1", Typeflag: tar.TypeReg}}, true},
		{"root file", []tar.Header{{Name: "snapshot_shard_paths", Typeflag: tar.TypeReg}}, false},
		{"other child", []tar.Header{{Name: "snapshot_shard_paths/ca.pem", Typeflag: tar.TypeReg}}, false},
		{"empty suffix", []tar.Header{{Name: "snapshot_shard_paths/snapshot_path_", Typeflag: tar.TypeReg}}, false},
		{"nested child", []tar.Header{{Name: metadata + "/child", Typeflag: tar.TypeReg}}, false},
		{"child directory", []tar.Header{{Name: metadata, Typeflag: tar.TypeDir}}, false},
		{"symlink", []tar.Header{{Name: metadata, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, false},
		{"hardlink", []tar.Header{{Name: metadata, Typeflag: tar.TypeLink, Linkname: "index-0"}}, false},
		{"traversal", []tar.Header{{Name: "snapshot_shard_paths/../index-0", Typeflag: tar.TypeReg}}, false},
		{"duplicate", []tar.Header{{Name: metadata, Typeflag: tar.TypeReg}, {Name: "./" + metadata, Typeflag: tar.TypeReg}}, false},
		{"oversized", []tar.Header{{Name: metadata, Typeflag: tar.TypeReg, Size: enginebackup.MaxPayload + 1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "snapshot.tar"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			archive := tar.NewWriter(file)
			for _, header := range tc.headers {
				if err := archive.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
			}
			if tc.headers[len(tc.headers)-1].Size == 0 {
				if err := archive.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateSnapshot(file); (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}

func TestNativeRestoreValidatesAllMetadataBeforeSelectingSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, engine, metadata, selector string
		valid                            bool
	}{
		{"OpenSearch stream", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events",".user-history",".ds-audit-history-000001"],"data_streams":["audit-history"]}]}`, "*", true},
		{"Elasticsearch stream", "elasticsearch", `{"snapshots":[{"state":"SUCCESS","indices":["events",".user-history",".ds-audit-history-000001"],"data_streams":["audit-history"]}]}`, "events,.user-history,audit-history", true},
		{"OpenSearch indices", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events",".user-history"],"data_streams":[]}]}`, "events,.user-history", true},
		{"protected index", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events",".opendistro_security"],"data_streams":["audit-history"]}]}`, "", false},
		{"unsupported index", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events","bad+index"],"data_streams":["audit-history"]}]}`, "", false},
		{"protected stream", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events"],"data_streams":["audit-history",".security-history"]}]}`, "", false},
		{"unsupported stream", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events"],"data_streams":["audit-history","bad+stream"]}]}`, "", false},
		{"plugin configuration index", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events",".opensearch-sap-log-types-config"],"data_streams":["audit-history"]}]}`, "", false},
		{"plugin configuration stream", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":["events"],"data_streams":["audit-history",".opensearch-sap-log-types-config"]}]}`, "", false},
		{"Elasticsearch same name", "elasticsearch", `{"snapshots":[{"state":"SUCCESS","indices":[".opensearch-sap-log-types-config"]}]}`, ".opensearch-sap-log-types-config", true},
		{"unbound backing index", "opensearch", `{"snapshots":[{"state":"SUCCESS","indices":[".ds-audit-history-000001"]}]}`, "", false},
		{"missing snapshot", "opensearch", `{}`, "", false},
		{"empty snapshot list", "opensearch", `{"snapshots":[]}`, "", false},
		{"multiple snapshots", "opensearch", `{"snapshots":[{"state":"SUCCESS"},{"state":"SUCCESS"}]}`, "", false},
		{"wrong snapshot type", "opensearch", `{"snapshots":{}}`, "", false},
		{"missing state", "opensearch", `{"snapshots":[{"indices":["events"]}]}`, "", false},
		{"incomplete state", "opensearch", `{"snapshots":[{"state":"PARTIAL","indices":["events"]}]}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "snapshot.tar"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			archive := tar.NewWriter(file)
			if err := archive.WriteHeader(&tar.Header{Name: "index-0", Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			var restore map[string]any
			db := Database{Engine: tc.engine, Extract: func(context.Context, *os.File, string) error { return nil }, Run: func(_ context.Context, args []string, out io.Writer) error {
				if args[0] == "mkdir" || args[0] == "rm" {
					return nil
				}
				method := ""
				var body []byte
				for i, arg := range args {
					if arg == "--request" {
						method = args[i+1]
					}
					if arg == "--data-binary" {
						body = []byte(args[i+1])
					}
				}
				response := `{"acknowledged":true}`
				switch method {
				case "GET":
					response = tc.metadata
				case "POST":
					if err := json.Unmarshal(body, &restore); err != nil {
						t.Fatal(err)
					}
					response = `{"snapshot":{"shards":{"total":3,"successful":3,"failed":0}}}`
				}
				_, err := io.WriteString(out, response+"\n200")
				return err
			}}
			if err := db.Restore(t.Context(), file); (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
			if !tc.valid {
				if restore != nil {
					t.Fatal("invalid metadata reached native restore")
				}
				return
			}
			if restore["indices"] != tc.selector || restore["include_global_state"] != false || restore["include_aliases"] != true {
				t.Fatalf("unsafe restore scope: %v", restore)
			}
			if _, exists := restore["ignore_unavailable"]; exists {
				t.Fatal("restore silently ignores unavailable content")
			}
			if tc.engine == "elasticsearch" {
				feature, ok := restore["feature_states"].([]any)
				if !ok || len(feature) != 1 || feature[0] != "none" {
					t.Fatal("security feature state not excluded")
				}
			}
		})
	}
}

func TestOpenSearchExcludesOnlyReservedLogTypeConfiguration(t *testing.T) {
	db := Database{Engine: "opensearch", Run: func(_ context.Context, args []string, out io.Writer) error {
		response := `{"data_streams":[]}`
		for _, arg := range args {
			if strings.Contains(arg, "/_cat/indices") {
				response = `[{"index":".opensearch-sap-log-types-config","system":"false"},{"index":".opensearch-sap-log-types-config-user","system":"false"},{"index":".opensearch-user-history","system":"false"},{"index":".user-history","system":"false"},{"index":"events","system":"false"}]`
			}
		}
		_, err := io.WriteString(out, response+"\n200")
		return err
	}}
	indices, err := db.userIndices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(indices, ",") != ".opensearch-sap-log-types-config-user,.opensearch-user-history,.user-history,events" {
		t.Fatalf("wrong user index scope: %v", indices)
	}
}
