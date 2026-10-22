// Package dbgateway generates the source of a Loomplane database gateway:
// a small Go program that speaks the Postgres wire protocol and the HTTP
// query API, and routes each connection to the right database. Loomplane
// builds it on the cloud's own builder, like any service, so it runs on
// your infrastructure and no query passes through Loomplane.
//
// The program is internal/dbgateway/plane plus a main function. The plane
// package is shipped as it stands (its tests and testdata stay behind), so
// what runs in your cloud is the code this repository tests.
package dbgateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

//go:embed plane/*.go program
var embedded embed.FS

// files maps each embedded program file to its name in the generated
// source. The Go files are stored under other names so this module
// doesn't treat the program as its own code, or as a module of its own.
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

// root is the source's one top-level directory, as in a GitHub tarball.
const root = "dbgateway"

// epoch is every file's modification time, so the same source always
// makes the same bytes, and the same ID.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Source is the database gateway's source as a gzipped tarball with one
// top-level directory, and an ID that changes only when the source does.
func Source() (data []byte, id string, err error) {
	contents := map[string][]byte{}
	for from, to := range files {
		b, rerr := fs.ReadFile(embedded, from)
		if rerr != nil {
			return nil, "", rerr
		}
		contents[to] = b
	}
	// The embed pattern takes the plane package's tests along with the
	// rest; they stay behind.
	planeFiles, err := fs.Glob(embedded, "plane/*.go")
	if err != nil {
		return nil, "", err
	}
	for _, from := range planeFiles {
		if strings.HasSuffix(from, "_test.go") {
			continue
		}
		b, rerr := fs.ReadFile(embedded, from)
		if rerr != nil {
			return nil, "", rerr
		}
		contents[from] = b
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
	dirs := []string{root + "/", root + "/plane/"}
	for _, dir := range dirs {
		if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: epoch}); err != nil {
			return nil, "", err
		}
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
	// Twelve characters, like the gateway's own ID.
	return buf.Bytes(), "dg" + hex.EncodeToString(sum.Sum(nil))[:10], nil
}
