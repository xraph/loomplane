// Package gateway generates the source of a Loomplane gateway: a small Go
// program on Bastion that routes paths to an application's services, with
// the routes fixed when it's deployed. Loomplane builds it on the cloud's
// own builder, like any service, so it runs on your infrastructure and no
// application traffic passes through Loomplane.
//
// The program has no admin API, dashboard or service discovery: the
// routes are all it knows, and each route's target address reaches it in
// a variable, the same way any service finds what it needs.
package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"time"
)

//go:embed program
var program embed.FS

// files maps each embedded file to its name in the generated source. The
// Go files are stored under other names so this module doesn't treat the
// program as its own code, or as a module of its own.
var files = map[string]string{
	"program/main.go.txt": "main.go",
	"program/go.mod.txt":  "go.mod",
	"program/go.sum.txt":  "go.sum",
	"program/Dockerfile":  "Dockerfile",
}

// Dockerfile is the Dockerfile's path in the source.
const Dockerfile = "Dockerfile"

// Port is the port the gateway listens on.
const Port = 8080

// Route sends requests under Path to a service.
type Route struct {
	Path string `json:"path"`
	// To is the service's name in the manifest.
	To string `json:"to"`
	// Env is the variable that holds the service's address.
	Env string `json:"env"`
	// StripPrefix removes Path before the request reaches the service.
	StripPrefix bool `json:"strip_prefix,omitempty"`
}

// root is the source's one top-level directory, as in a GitHub tarball.
const root = "gateway"

// epoch is every file's modification time, so the same routes always
// make the same bytes, and the same ID.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Source is the gateway's source for routes, as a gzipped tarball with one
// top-level directory, and an ID that changes only when the source does.
func Source(routes []Route) (data []byte, id string, err error) {
	sorted := append([]Route(nil), routes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	table, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return nil, "", err
	}
	contents := map[string][]byte{"routes.json": append(table, '\n')}
	for from, to := range files {
		b, rerr := fs.ReadFile(program, from)
		if rerr != nil {
			return nil, "", rerr
		}
		contents[to] = b
	}
	names := make([]string, 0, len(contents))
	for name := range contents {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.ModTime = epoch
	tw := tar.NewWriter(zw)
	sum := sha256.New()
	if err := tw.WriteHeader(&tar.Header{Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: epoch}); err != nil {
		return nil, "", err
	}
	for _, name := range names {
		b := contents[name]
		if err := tw.WriteHeader(&tar.Header{Name: path.Join(root, name), Mode: 0o644, Size: int64(len(b)), ModTime: epoch}); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, "", err
		}
		fmt.Fprintf(sum, "%s\x00%d\x00", name, len(b))
		sum.Write(b)
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	// Twelve characters, like the commit prefix a service's image is
	// tagged with.
	return buf.Bytes(), "gw" + hex.EncodeToString(sum.Sum(nil))[:10], nil
}
