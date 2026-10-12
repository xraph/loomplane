// Package statichost is an example Loomplane provider: it hosts static
// sites itself, from the files in the repository, and serves them at
// /sites/<project>/<environment>/<name>/. It's small enough to read in one
// sitting, and it's a real provider: Loomplane plans for it, deploys to it
// and checks it for drift like any other target.
//
// It serves a site's built output when the repository has it (dist, build
// or out in the site's directory) and the directory as it is otherwise. It
// never runs a build: that would mean running the repository's code.
package statichost

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xraph/loomplane/pkg/provider"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

// Host is the provider and the server for the sites it hosts.
type Host struct {
	// Dir is where sites are kept.
	Dir string
	// BaseURL is where Host's HTTP server is reachable, for the URLs it
	// hands back.
	BaseURL string
	// Token is the credential a connection to it must present.
	Token string
}

var _ sdk.Provider = (*Host)(nil)
var _ sdk.Connector = (*Host)(nil)

// Describe implements sdk.Provider.
func (h *Host) Describe() provider.Provider {
	return provider.Provider{
		Name: "static-host", Title: "Static host", Cloud: "static-host",
		Description: "Static sites served from their files, by an example provider plugin.",
		Model:       provider.Build,
		Runs:        []provider.Runs{{Workload: provider.StaticSite, Execution: provider.Serverless, Creates: "Static site"}},
	}
}

// ConnectionForm implements sdk.Connector.
func (h *Host) ConnectionForm() sdk.ConnectionForm {
	return sdk.ConnectionForm{
		Title: "Static host", Credential: "Access token",
		Help: "The token the static host was started with.",
	}
}

// Check implements sdk.Provider: the credential is the host's token.
func (h *Host) Check(_ context.Context, c sdk.Credential) (*sdk.Account, error) {
	if string(c.Secret) != h.Token {
		return nil, fmt.Errorf("%w: that isn't this host's token", sdk.ErrRejected)
	}
	return &sdk.Account{Account: h.BaseURL, Details: map[string]string{"host": h.BaseURL}}, nil
}

func (h *Host) site(project, env, name string) (string, error) {
	for _, part := range []string{project, env, name} {
		if part == "" || strings.ContainsAny(part, `/\`) || part == "." || part == ".." {
			return "", fmt.Errorf("%q can't name a site", part)
		}
	}
	return filepath.Join(h.Dir, project, env, name), nil
}

// Apply implements sdk.Provider: it unpacks the site's files into a new
// directory and swaps it in, so a site is never half replaced.
func (h *Host) Apply(_ context.Context, job *sdk.Job) (*sdk.Outcome, error) {
	if job.Service == nil {
		return nil, errors.New("the static host hosts sites, not resources")
	}
	dir, err := h.site(job.Project, job.Environment, job.Name)
	if err != nil {
		return nil, err
	}
	job.Progress("Unpacking the site")
	src, err := job.Source()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	staging := dir + ".new"
	_ = os.RemoveAll(staging)
	digest, err := unpack(src, path.Clean(job.Service.Source), staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, dir); err != nil {
		return nil, err
	}
	return &sdk.Outcome{
		ExternalID: path.Join(job.Project, job.Environment, job.Name),
		URL:        h.BaseURL + "/sites/" + path.Join(job.Project, job.Environment, job.Name) + "/",
		Revision:   digest,
	}, nil
}

// unpack writes the files under dir in the tarball to dest, preferring
// the built output, and returns a digest of what it wrote.
func unpack(src io.Reader, dir, dest string) (string, error) {
	zr, err := gzip.NewReader(src)
	if err != nil {
		return "", err
	}
	files := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// GitHub's tarballs put everything under one directory.
		_, name, _ := strings.Cut(hdr.Name, "/")
		rel, ok := strings.CutPrefix(name, dir+"/")
		if !ok || hdr.Size > 32<<20 {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return "", err
		}
		files[rel] = b
	}
	for _, out := range []string{"dist", "build", "out"} {
		built := map[string][]byte{}
		for name, b := range files {
			if rel, ok := strings.CutPrefix(name, out+"/"); ok {
				built[rel] = b
			}
		}
		if len(built) > 0 {
			files = built
			break
		}
	}
	if len(files) == 0 {
		return "", fmt.Errorf("the repository has no files under %s", dir)
	}
	sum := sha256.New()
	for name, b := range files {
		p := filepath.Join(dest, filepath.FromSlash(name))
		if !strings.HasPrefix(p, filepath.Clean(dest)+string(filepath.Separator)) {
			continue
		}
		// The files are served to anyone, so they're readable by anyone.
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: public files
			return "", err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil { //nolint:gosec // G306: public files
			return "", err
		}
		fmt.Fprintf(sum, "%s %d\n", name, len(b))
		sum.Write(b)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}

// Remove implements sdk.Provider.
func (h *Host) Remove(_ context.Context, job *sdk.Job) error {
	if job.Current == nil {
		return nil
	}
	parts := strings.Split(job.Current.ExternalID, "/")
	if len(parts) != 3 {
		return fmt.Errorf("%q isn't one of this host's sites", job.Current.ExternalID)
	}
	dir, err := h.site(parts[0], parts[1], parts[2])
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Observe implements sdk.Provider.
func (h *Host) Observe(_ context.Context, t *sdk.Target) (*sdk.Observation, error) {
	parts := strings.Split(t.ExternalID, "/")
	if len(parts) != 3 {
		return &sdk.Observation{}, nil
	}
	dir, err := h.site(parts[0], parts[1], parts[2])
	if err != nil {
		return &sdk.Observation{}, nil //nolint:nilerr // not one of ours, so not live
	}
	if _, err := os.Stat(dir); err != nil {
		return &sdk.Observation{}, nil //nolint:nilerr // gone
	}
	return &sdk.Observation{Exists: true, Revision: t.Revision}, nil
}

// Handler serves the provider protocol under /v1/ and the sites under
// /sites/.
func (h *Host) Handler(token string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", sdk.Handler(h, token))
	mux.Handle("/sites/", http.StripPrefix("/sites/", http.FileServer(http.Dir(h.Dir))))
	return mux
}
