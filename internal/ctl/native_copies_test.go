package ctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCopyCLIReviewAdmissionStatusAndCleanupKeepOneRequest(t *testing.T) {
	reviewFile := filepath.Join(t.TempDir(), "review.json")
	creates, cleans := 0, 0
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/native-recovery-sources/source/points/point/copy-review":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["request_id"] != "stable-native" || body["cloud_id"] != "cloud" || body["target_id"] != "target" || body["ttl_hours"] != float64(2) {
				t.Error("lost reviewed placement", body)
			}
			fmt.Fprint(w, `{"request_id":"stable-native","review_token":"signed-review","source_id":"source","point_id":"point","ttl_hours":2,"notice":"Charges continue until deletion is confirmed.","capacity":{"storage_gb":10}}`)
		case "POST /api/v1/native-recovery-copies":
			creates++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["request_id"] != "stable-native" || body["review_token"] != "signed-review" || body["acknowledge_charges"] != true {
				t.Error("lost reviewed request", body)
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"nativecopy","status":"queued","expires_at":"2026-10-24T01:00:00Z"}`)
		case "GET /api/v1/native-recovery-copies/nativecopy":
			fmt.Fprint(w, `{"id":"nativecopy","status":"failed","error":"target denied","expires_at":"2026-10-24T01:00:00Z"}`)
		case "DELETE /api/v1/native-recovery-copies/nativecopy":
			cleans++
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"nativecopy","status":"deleting"}`)
		default:
			t.Error("unexpected request", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	reviewArgs := []string{"backups", "native-copies", "review", "source", "point", "--cloud", "cloud", "--target", "target", "--ttl-hours", "2", "--request-id", "stable-native", "--out", reviewFile}
	if err := app.Run(reviewArgs); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(reviewFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private review file: %v %v", info, err)
	}
	saved, err := os.ReadFile(reviewFile)
	if err != nil || !strings.Contains(string(saved), "signed-review") || (!strings.Contains(out.String(), "Charges continue") || !strings.Contains(out.String(), "10 GiB") || !strings.Contains(out.String(), "2h")) {
		t.Fatalf("missing review: %s %s %v", saved, out, err)
	}
	if err := app.Run(reviewArgs); err == nil {
		t.Fatal("overwrote an existing review file")
	}
	createArgs := make([]string, 0, 7)
	createArgs = append(createArgs, "backups", "native-copies", "create", "--review-file", reviewFile)
	if err := app.Run(createArgs); err == nil || creates != 0 {
		t.Fatal("admitted without charge acknowledgement")
	}
	for range 2 {
		if err := app.Run(append(createArgs, "--acknowledge-charges")); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 2 || !strings.Contains(out.String(), "2026-10-24T01:00:00Z") {
		t.Fatal("retry or retained expiry lost", out)
	}
	if err := app.Run([]string{"backups", "native-copies", "show", "nativecopy", "--wait"}); err == nil || !strings.Contains(err.Error(), "target denied") {
		t.Fatalf("failed copy reported success: %v", err)
	}
	cleanupArgs := make([]string, 0, 5)
	cleanupArgs = append(cleanupArgs, "backups", "native-copies", "cleanup", "nativecopy")
	if err := app.Run(cleanupArgs); err == nil || cleans != 0 {
		t.Fatal("cleaned up without confirmation")
	}
	if err := app.Run(append(cleanupArgs, "--yes")); err != nil || cleans != 1 {
		t.Fatalf("cleanup request: %v", err)
	}
}

func TestNativeCopyCLIRejectsInvalidReviewAndShowsScopedPage(t *testing.T) {
	calls := 0
	app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/v1/projects" {
			fmt.Fprint(w, `{"projects":[{"id":"project","slug":"database"}]}`)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/projects/project/native-recovery-copies" || r.URL.Query().Get("environment_id") != "env" || r.URL.Query().Get("before") != "cursor" || r.URL.Query().Get("limit") != "1" {
			t.Error("lost page scope", r.URL)
		}
		fmt.Fprint(w, `{"copies":[{"id":"copy","status":"cleanup_failed","error":"retained","target":{"provider":"cloud-sql"}}],"next_before":"next-copy"}`)
	})
	for _, args := range [][]string{
		{"review", "source", "point", "--cloud", "cloud", "--target", "target", "--ttl-hours", "25"},
		{"review", "source", "point", "--cloud", "cloud"},
		{"show"}, {"cleanup", "copy"}, {"create", "--acknowledge-charges"},
	} {
		if err := app.Run(append([]string{"backups", "native-copies"}, args...)); err == nil {
			t.Fatal("accepted invalid request", args)
		}
	}
	if calls != 0 {
		t.Fatal("invalid CLI input reached API")
	}
	file := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(file, []byte(`{"request_id":"x","review_token":"y"} trailing`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.Run([]string{"backups", "native-copies", "create", "--review-file", file, "--acknowledge-charges"}); err == nil || calls != 0 {
		t.Fatal("accepted trailing review input")
	}
	if err := app.Run([]string{"backups", "native-copies", "database", "--environment-id", "env", "--before", "cursor", "--limit", "1", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "next-copy") || !strings.Contains(out.String(), "cleanup_failed") {
		t.Fatal("lost copy history", out)
	}
}
