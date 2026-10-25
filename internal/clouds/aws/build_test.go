package aws

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageDigest_readsECR(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if !strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".DescribeImages") {
			t.Errorf("asked %s", r.Header.Get("X-Amz-Target"))
		}
		var in struct {
			RepositoryName string `json:"repositoryName"`
			ImageIDs       []struct {
				ImageTag string `json:"imageTag"`
			} `json:"imageIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.RepositoryName != "loomplane/api-x" || len(in.ImageIDs) != 1 {
			t.Errorf("asked for %+v", in)
		}
		if in.ImageIDs[0].ImageTag != "abc123def456" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"ImageNotFoundException","message":"The image requested does not exist"}`))
			return
		}
		_, _ = w.Write([]byte(`{"imageDetails":[{"repositoryName":"loomplane/api-x","imageDigest":"` + digest + `","imageTags":["abc123def456"]}]}`))
	}))
	defer srv.Close()
	s := New(srv.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "123456789012")

	got, err := s.ImageDigest(t.Context(), "loomplane/api-x", "abc123def456")
	if err != nil || got != digest {
		t.Fatalf("digest %q, %v", got, err)
	}
	if _, err := s.ImageDigest(t.Context(), "loomplane/api-x", "000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown tag: %v", err)
	}
}
