// Package search uses native filesystem snapshots without cluster security state.
package search

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/dbexec"
	"github.com/xraph/loomplane/internal/clouds/enginebackup"
)

type Database struct {
	Engine   string
	Run      dbexec.Run
	Upload   dbexec.Upload
	Download func(context.Context, string, io.Writer) error
	Extract  func(context.Context, *os.File, string) error
}

func (d Database) root() string { return "/usr/share/" + d.Engine + "/data/loomplane-snapshots" }
func (d Database) api(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	args := []string{"--silent", "--show-error", "--fail", "--max-time", "300", "--write-out", "\n%{http_code}", "--request", method}
	scheme := "http"
	if d.Engine == "opensearch" {
		scheme = "https"
	}
	args = append(args, "--url", scheme+"://localhost:9200"+endpoint)
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		// Native requests here contain index names and recovery settings only.
		args = append(args, "--header", "Content-Type: application/json", "--data-binary", string(raw))
	}
	var out bytes.Buffer
	runErr := d.Run(ctx, d.command(args...), &limitWriter{dst: &out, left: 4 << 20})
	raw := out.Bytes()
	split := bytes.LastIndexByte(raw, '\n')
	status := 0
	if split >= 0 {
		var parseErr error
		status, parseErr = strconv.Atoi(string(raw[split+1:]))
		if parseErr != nil {
			return nil, errors.New("native search returned an invalid HTTP status")
		}
	}
	if runErr != nil || status < 200 || status >= 300 {
		return nil, &requestError{method: method, endpoint: endpoint, status: status}
	}
	return raw[:split], nil
}
func (d Database) repository(ctx context.Context, name string, readonly bool) error {
	_, err := d.api(ctx, "PUT", "/_snapshot/"+name, map[string]any{"type": "fs", "settings": map[string]any{"location": d.root() + "/" + name, "readonly": readonly}})
	return err
}

type requestError struct {
	method, endpoint string
	status           int
}

func (e *requestError) Error() string {
	return fmt.Sprintf("native search %s %s failed (HTTP %d)", e.method, e.endpoint, e.status)
}
func missingSnapshot(err error) bool {
	var failure *requestError
	return errors.As(err, &failure) && failure.status == 404
}
func (d Database) cleanup(ctx context.Context, name string, writable bool) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if writable {
		if _, err := d.api(cleanup, "DELETE", "/_snapshot/"+name+"/backup", nil); err != nil && !missingSnapshot(err) {
			return err
		}
	}
	if _, err := d.api(cleanup, "DELETE", "/_snapshot/"+name, nil); err != nil && !missingSnapshot(err) {
		return err
	}
	return d.Run(cleanup, []string{"rm", "-rf", "--", d.root() + "/" + name}, io.Discard)
}
func (d Database) Export(ctx context.Context, dst io.Writer) (err error) {
	name := "lp-" + rand.Text()
	if checkErr := d.Run(ctx, []string{"mkdir", "-p", d.root() + "/" + name}, io.Discard); checkErr != nil {
		return checkErr
	}
	defer func() { err = errors.Join(err, d.cleanup(ctx, name, true)) }()
	if checkErr := d.repository(ctx, name, false); checkErr != nil {
		return checkErr
	}
	indices, err := d.userIndices(ctx)
	if err != nil {
		return err
	}
	selector := "-_all"
	if len(indices) > 0 {
		selector = strings.Join(indices, ",")
	}
	body := map[string]any{"indices": selector, "include_global_state": false, "partial": false}
	if d.Engine == "elasticsearch" {
		body["feature_states"] = []string{"none"}
	}
	raw, err := d.api(ctx, "PUT", "/_snapshot/"+name+"/backup?wait_for_completion=true", body)
	if err != nil {
		return err
	}
	var response struct {
		Snapshot struct {
			State string `json:"state"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Snapshot.State != "SUCCESS" {
		return errors.New("native search snapshot did not complete")
	}
	return d.Download(ctx, d.root()+"/"+name, dst)
}

var validIndex = regexp.MustCompile(`^[a-z0-9.][a-z0-9_.-]*$`)

func protectedIndex(name string) bool {
	return strings.HasPrefix(name, ".security") || strings.HasPrefix(name, ".opendistro_security")
}
func (d Database) command(args ...string) []string {
	return append([]string{"sh", "-c", `config=$1; shift; sed '/^url = /d' "$config" | curl --config - "$@"`, "loomplane-search", "/usr/share/" + d.Engine + "/config/loomplane-probe.conf"}, args...)
}
func (d Database) userIndices(ctx context.Context) ([]string, error) {
	indices := []string{}
	if d.Engine == "opensearch" {
		raw, err := d.api(ctx, "GET", "/_cat/indices?format=json&expand_wildcards=all&system=false&h=index,system", nil)
		if err != nil {
			return nil, err
		}
		var entries []struct {
			Index  string `json:"index"`
			System string `json:"system"`
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.System != "false" {
				return nil, errors.New("search did not classify its user indices")
			}
			indices = append(indices, entry.Index)
		}
	} else {
		raw, err := d.api(ctx, "GET", "/_resolve/index/*?expand_wildcards=all", nil)
		if err != nil {
			return nil, err
		}
		var entries struct {
			Indices []struct {
				Name       string   `json:"name"`
				Attributes []string `json:"attributes"`
			} `json:"indices"`
			Streams []json.RawMessage `json:"data_streams"`
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		for _, entry := range entries.Indices {
			if !slices.Contains(entry.Attributes, "system") {
				indices = append(indices, entry.Name)
			}
		}
	}
	admitted := map[string]bool{}
	for _, index := range indices {
		admitted[index] = true
	}
	streamPath := "/_data_stream/*?expand_wildcards=all"
	if d.Engine == "opensearch" {
		streamPath = "/_data_stream/*"
	}
	raw, err := d.api(ctx, "GET", streamPath, nil)
	if err != nil {
		return nil, err
	}
	var streams struct {
		Streams []struct {
			Name    string `json:"name"`
			Indices []struct {
				Name string `json:"index_name"`
			} `json:"indices"`
		} `json:"data_streams"`
	}
	if err := json.Unmarshal(raw, &streams); err != nil {
		return nil, err
	}
	for _, stream := range streams.Streams {
		user := len(stream.Indices) > 0
		for _, index := range stream.Indices {
			user = user && admitted[index.Name]
		}
		if !user {
			continue
		}
		for _, index := range stream.Indices {
			delete(admitted, index.Name)
		}
		admitted[stream.Name] = true
	}
	indices = indices[:0]
	for index := range admitted {
		if !validIndex.MatchString(index) || protectedIndex(index) || strings.HasPrefix(index, ".ds-") {
			return nil, errors.New("unsupported or protected user index")
		}
		indices = append(indices, index)
	}
	slices.Sort(indices)
	return indices, nil
}

var snapshotPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidateSnapshot checks every path before tar can extract any native files.
func ValidateSnapshot(file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	archive := tar.NewReader(file)
	seen := map[string]bool{}
	var size int64
	for entries := 0; ; entries++ {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if entries >= 100000 {
			return errors.New("snapshot has too many files")
		}
		name := strings.TrimPrefix(h.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "." || name == "" {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid snapshot root")
		}
		if path.Clean(name) != name || strings.HasPrefix(name, "/") || len(name) > 1024 || seen[name] {
			return errors.New("invalid snapshot path")
		}
		seen[name] = true
		parts := strings.Split(name, "/")
		if len(parts) > 8 {
			return errors.New("invalid snapshot depth")
		}
		for _, part := range parts {
			if part == "." || part == ".." || !snapshotPart.MatchString(part) {
				return errors.New("invalid snapshot file name")
			}
		}
		if parts[0] == "snapshot_shard_paths" {
			directory := len(parts) == 1 && h.Typeflag == tar.TypeDir
			metadata := len(parts) == 2 && h.Typeflag == tar.TypeReg && strings.HasPrefix(parts[1], "snapshot_path_") && len(parts[1]) > len("snapshot_path_")
			if !directory && !metadata {
				return fmt.Errorf("unexpected snapshot shard path %q", name)
			}
		} else if parts[0] != "indices" && !strings.HasPrefix(parts[0], "index-") && parts[0] != "index.latest" && !strings.HasPrefix(parts[0], "snap-") && !strings.HasPrefix(parts[0], "meta-") {
			return fmt.Errorf("unexpected snapshot file %q", name)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return errors.New("snapshot links and special files are forbidden")
		}
		if h.Typeflag == tar.TypeDir && h.Size != 0 {
			return errors.New("invalid snapshot directory")
		}
		size += h.Size
		if h.Size < 0 || size > enginebackup.MaxPayload {
			return errors.New("snapshot exceeds size limit")
		}
	}
	if len(seen) == 0 {
		return errors.New("empty snapshot repository")
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}
func (d Database) Restore(ctx context.Context, payload *os.File) (err error) {
	if checkErr := ValidateSnapshot(payload); checkErr != nil {
		return checkErr
	}
	name := "lp-" + rand.Text()
	if checkErr := d.Run(ctx, []string{"mkdir", "-p", d.root() + "/" + name}, io.Discard); checkErr != nil {
		return checkErr
	}
	defer func() { err = errors.Join(err, d.cleanup(ctx, name, false)) }()
	if checkErr := d.Extract(ctx, payload, d.root()+"/"+name); checkErr != nil {
		return checkErr
	}
	if checkErr := d.repository(ctx, name, true); checkErr != nil {
		return checkErr
	}
	metadata, err := d.api(ctx, "GET", "/_snapshot/"+name+"/backup", nil)
	if err != nil {
		return err
	}
	var snapshot struct {
		Snapshots []struct {
			State   string   `json:"state"`
			Indices []string `json:"indices"`
			Streams []string `json:"data_streams"`
		} `json:"snapshots"`
	}
	if json.Unmarshal(metadata, &snapshot) != nil || len(snapshot.Snapshots) != 1 || snapshot.Snapshots[0].State != "SUCCESS" {
		return errors.New("invalid native snapshot metadata")
	}
	selections := []string{}
	for _, index := range snapshot.Snapshots[0].Indices {
		if protectedIndex(index) || !validIndex.MatchString(index) {
			return errors.New("snapshot contains a protected or unsupported index")
		}
		if strings.HasPrefix(index, ".ds-") {
			if len(snapshot.Snapshots[0].Streams) == 0 {
				return errors.New("snapshot has an unbound data stream index")
			}
			continue
		}
		selections = append(selections, index)
	}
	for _, stream := range snapshot.Snapshots[0].Streams {
		if protectedIndex(stream) || !validIndex.MatchString(stream) {
			return errors.New("snapshot contains an unsupported data stream")
		}
		selections = append(selections, stream)
	}
	selector := "-_all"
	if len(selections) > 0 {
		selector = strings.Join(selections, ",")
	}
	body := map[string]any{"indices": selector, "include_global_state": false, "include_aliases": true}
	if d.Engine == "elasticsearch" {
		body["feature_states"] = []string{"none"}
	}
	raw, err := d.api(ctx, "POST", "/_snapshot/"+name+"/backup/_restore?wait_for_completion=true", body)
	if err != nil {
		return err
	}
	var result struct {
		Snapshot struct {
			Shards struct {
				Failed     int `json:"failed"`
				Total      int `json:"total"`
				Successful int `json:"successful"`
			} `json:"shards"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Snapshot.Shards.Failed != 0 || result.Snapshot.Shards.Successful != result.Snapshot.Shards.Total {
		return errors.New("native search restore failed")
	}
	return nil
}

// ConfigureReader grants a fresh copy credential only user-index read access.
func (d Database) ConfigureReader(ctx context.Context, password string) (err error) {
	if checkErr := dbexec.ValidatePassword(password); checkErr != nil {
		return checkErr
	}
	var rolePath, userPath string
	var role, user any
	if d.Engine == "elasticsearch" {
		rolePath = "/_security/role/loomplane_recovery"
		userPath = "/_security/user/loomplane_recovery"
		role = map[string]any{"cluster": []string{}, "indices": []any{map[string]any{"names": []string{"*"}, "privileges": []string{"read", "view_index_metadata"}, "allow_restricted_indices": false}}}
		user = map[string]any{"password": password, "roles": []string{"loomplane_recovery"}}
	} else {
		// OpenSearch's security REST API is disabled for normal runtime users.
		// Its immutable recovery reader is rendered in the clone's security files.
		return nil
	}
	if _, err = d.api(ctx, "PUT", rolePath, role); err != nil {
		return err
	}
	raw, err := json.Marshal(user)
	if err != nil {
		return err
	}
	temp := "loomplane-reader-" + rand.Text()
	defer func() { err = errors.Join(err, dbexec.Cleanup(ctx, d.Run, temp)) }()
	if checkErr := d.Upload(ctx, temp, int64(len(raw)), func(w io.Writer) error { _, e := w.Write(raw); return e }); checkErr != nil {
		return checkErr
	}
	args := []string{"--silent", "--show-error", "--fail", "--request", "PUT", "--url", "http://localhost:9200" + userPath, "--header", "Content-Type: application/json", "--data-binary", "@/tmp/" + temp}
	if err = d.Run(ctx, d.command(args...), io.Discard); err != nil {
		return fmt.Errorf("configure recovery reader: %w", err)
	}
	return nil
}

type limitWriter struct {
	dst  io.Writer
	left int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, errors.New("search reply exceeds limit")
	}
	n, e := w.dst.Write(p)
	w.left -= int64(n)
	return n, e
}
