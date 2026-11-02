package server

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
)

// uploadFiles writes a container's rendered files into it before it
// starts, owned by root with their modes. Each directory's files go in
// one archive extracted there. The archive API extracts only into a
// directory that exists, so one the image lacks is created from the root.
func uploadFiles(ctx context.Context, c *Client, container string, values owned.RuntimeValues) error {
	dirs := map[string][]string{}
	for _, p := range slices.Sorted(maps.Keys(values.Files)) {
		if !path.IsAbs(p) || path.Clean(p) != p || p == "/" {
			return fmt.Errorf("the rendered file %q isn't a clean absolute path", p)
		}
		dirs[path.Dir(p)] = append(dirs[path.Dir(p)], p)
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		err := putArchive(ctx, c, container, dir, dirs[dir], values)
		if errors.Is(err, ErrMissing) && dir != "/" {
			err = putArchive(ctx, c, container, "/", dirs[dir], values)
		}
		if err != nil {
			return fmt.Errorf("write the rendered files in %s: %w", dir, err)
		}
	}
	return nil
}

// putArchive extracts files into dir in the container, named relative to
// it.
func putArchive(ctx context.Context, c *Client, container, dir string, files []string, values owned.RuntimeValues) error {
	var body bytes.Buffer
	archive := tar.NewWriter(&body)
	for _, p := range files {
		data := values.Files[p]
		header := &tar.Header{Typeflag: tar.TypeReg, Name: strings.TrimPrefix(strings.TrimPrefix(p, dir), "/"), Mode: int64(values.FileMode(p)), Size: int64(len(data)), ModTime: time.Now()}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(data); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	res, err := c.RequestHeaders(ctx, http.MethodPut, "/containers/"+url.PathEscape(container)+"/archive?path="+url.QueryEscape(dir), &body, http.Header{"Content-Type": {"application/x-tar"}})
	if err != nil {
		return err
	}
	return dockerResult(res, nil)
}
