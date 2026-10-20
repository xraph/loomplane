// Package backupfiles transfers backup objects through a dedicated Trove store.
// Backup stores must not install whole-object middleware: native multipart
// operations use Trove's selected driver and do not run that middleware.
package backupfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/xraph/trove"
	"github.com/xraph/trove/driver"
)

const partSize = 8 << 20
const maxParts = 10000
const maxBytes = int64(partSize) * maxParts

// Object pins bytes for a recovery point. Persist it only after Put succeeds.
type Object struct {
	Key           string `json:"key"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	VersionID     string `json:"version_id,omitempty"`
	DestinationID string `json:"destination_id,omitempty"`
}

// Validate checks captured key and integrity metadata without accessing storage.
// It does not establish that the stored bytes match the recorded checksum.
func (o Object) Validate() error {
	if err := validateKey(o.Key); err != nil {
		return err
	}
	if o.DestinationID != "" {
		if _, valid := decodeDigest(o.DestinationID); !valid {
			return errors.New("invalid backup destination identity")
		}
	}
	_, err := o.integrity()
	return err
}

func (o Object) integrity() ([]byte, error) {
	expected, valid := decodeDigest(o.SHA256)
	if !valid || o.Size < 0 || o.Size > maxBytes {
		return nil, errors.New("invalid backup integrity metadata")
	}
	return expected, nil
}

func decodeDigest(value string) ([]byte, bool) {
	expected, err := hex.DecodeString(value)
	return expected, err == nil && len(expected) == sha256.Size
}

// Store borrows the extension's backups store; its owner handles shutdown.
type Store struct {
	Trove       *trove.Trove
	Bucket      string
	destination *destination
}

func (s *Store) validate(key string) error {
	if s.Trove == nil || s.Bucket == "" {
		return errors.New("backup storage is not configured")
	}
	return validateKey(key)
}

func validateKey(key string) error {
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "/") || key == "." || key == ".." || strings.HasPrefix(key, "../") || strings.ContainsAny(key, "\\\x00") {
		return errors.New("invalid backup object key")
	}
	return nil
}

// Put uploads a uniquely named backup. Memory is bounded by one 8 MiB part
// plus the driver's part buffer. Incomplete multipart uploads are aborted.
func (s *Store) Put(ctx context.Context, key string, source io.Reader) (Object, error) {
	if err := s.validate(key); err != nil {
		return Object{}, err
	}
	destinationID, err := s.destinationID(key)
	if err != nil {
		return Object{}, err
	}
	hash := sha256.New()
	input := io.TeeReader(&contextReader{ctx: ctx, r: &boundedReader{r: source, remaining: maxBytes}}, hash)
	drv := s.Trove.DriverFor(s.Bucket, key)
	var info *driver.ObjectInfo
	if multipart, ok := drv.(driver.MultipartDriver); ok {
		info, err = s.multipart(ctx, multipart, key, input)
	} else if drv.Name() == "local" || drv.Name() == "mem" {
		// The local driver commits with an atomic rename; memory is test-only.
		info, err = s.Trove.Put(ctx, s.Bucket, key, input, driver.WithContentType("application/octet-stream"))
	} else {
		return Object{}, errors.New("backup driver needs multipart support")
	}
	if err != nil {
		return Object{}, err
	}
	if info == nil || info.Size < 0 || info.Size > maxBytes {
		return Object{}, errors.New("invalid completed backup metadata")
	}
	return Object{Key: key, Size: info.Size, SHA256: hex.EncodeToString(hash.Sum(nil)), VersionID: info.VersionID, DestinationID: destinationID}, nil
}

func (s *Store) multipart(ctx context.Context, drv driver.MultipartDriver, key string, input io.Reader) (info *driver.ObjectInfo, err error) {
	upload, err := drv.InitiateMultipart(ctx, s.Bucket, key, driver.WithContentType("application/octet-stream"))
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			err = errors.Join(err, drv.AbortMultipart(cleanup, s.Bucket, key, upload))
		}
	}()
	buffer := make([]byte, partSize)
	parts := make([]driver.PartInfo, 0, 16)
	var total int64
	for {
		n, readErr := io.ReadFull(input, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, readErr
		}
		if n == 0 && len(parts) > 0 {
			break
		}
		if len(parts) >= maxParts {
			return nil, fmt.Errorf("backup exceeds %d bytes", maxBytes)
		}
		var part *driver.PartInfo
		for attempt := range 3 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			part, err = drv.UploadPart(ctx, s.Bucket, key, upload, len(parts)+1, bytes.NewReader(buffer[:n]))
			if err == nil {
				break
			}
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
				}
			}
		}
		if err != nil {
			return nil, err
		}
		if part == nil || part.Size != int64(n) || part.PartNumber != len(parts)+1 {
			return nil, errors.New("backup part metadata does not match its bytes")
		}
		parts = append(parts, *part)
		total += int64(n)
		if readErr != nil {
			break
		}
	}
	info, err = drv.CompleteMultipart(ctx, s.Bucket, key, upload, parts)
	if err != nil {
		return nil, err
	}
	complete = true
	if info == nil || info.Size != total {
		return nil, errors.New("completed backup size does not match its bytes")
	}
	return info, nil
}

// CopyVerified checks size and SHA-256 while copying. The destination must be
// isolated until this returns nil, since corruption can be detected at EOF.
func (s *Store) CopyVerified(ctx context.Context, ref Object, dst io.Writer) error {
	if err := s.validate(ref.Key); err != nil {
		return err
	}
	if err := s.checkDestination(ref, false); err != nil {
		return err
	}
	expected, err := ref.integrity()
	if err != nil {
		return err
	}
	object, err := s.Trove.Get(ctx, s.Bucket, ref.Key, driver.WithVersionID(ref.VersionID))
	if err != nil {
		return err
	}
	defer object.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(&contextReader{ctx: ctx, r: object}, ref.Size+1))
	if err != nil {
		return err
	}
	if n != ref.Size || !bytes.Equal(hash.Sum(nil), expected) {
		return errors.New("backup integrity check failed")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

// Probe past the limit so a driver cannot commit a silently truncated object.
type boundedReader struct {
	r         io.Reader
	remaining int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.r.Read(extra[:])
		if n > 0 {
			return 0, fmt.Errorf("backup exceeds %d bytes", maxBytes)
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
