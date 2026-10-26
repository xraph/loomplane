package federation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrImageProof = errors.New("runtime image cannot be proved from bounded registry manifests")

// CollectImageIndexes retrieves only digest-addressed manifests in the pinned
// image's repository. Fetch must enforce the reviewed registry connection. Raw
// bytes are hash checked before parsing, and the existing verifier decides proof
// validity. Runtime repository aliases and descriptor URLs never select a host.
func CollectImageIndexes(ctx context.Context, reference, reported, operatingSystem, architecture string, fetch func(context.Context, string) ([]byte, error)) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidPinnedImage(reference) || !ValidPinnedImage(reported) {
		return nil, ErrImageProof
	}
	if _, ok := ResolveImageSelector(reference, reported, operatingSystem, architecture, nil); ok {
		return nil, nil
	}
	if fetch == nil || !ValidIdentitySegment(operatingSystem) || !ValidIdentitySegment(architecture) || operatingSystem == "unknown" || architecture == "unknown" {
		return nil, ErrImageProof
	}
	repository, root, _ := strings.Cut(reference, "@")
	_, target, _ := strings.Cut(reported, "@")
	proofs := [][]byte{}
	seen := map[string]bool{}
	calls := 0
	var visit func(string) (bool, error)
	visit = func(digest string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if seen[digest] {
			return false, nil
		}
		if calls >= MaxImageIndexes || !ValidPinnedImage("image@"+digest) {
			return false, ErrImageProof
		}
		seen[digest] = true
		calls++
		raw, err := fetch(ctx, repository+"@"+digest)
		if err != nil {
			return false, err
		}
		if len(raw) == 0 || len(raw) > MaxImageIndexBytes {
			return false, ErrImageProof
		}
		sum := sha256.Sum256(raw)
		if "sha256:"+hex.EncodeToString(sum[:]) != digest {
			return false, ErrImageProof
		}
		index, valid := parseImageIndex(raw)
		if !valid {
			return false, nil
		}
		proofs = append(proofs, bytes.Clone(raw))
		if _, ok := ResolveImageSelector(reference, reported, operatingSystem, architecture, proofs); ok {
			return true, nil
		}
		for _, descriptor := range index.Manifests {
			if descriptor.MediaType != ociImageIndex && descriptor.MediaType != dockerImageIndex {
				continue
			}
			if descriptor.Size <= 0 || descriptor.Size > MaxImageIndexBytes {
				continue
			}
			platform := descriptor.Platform
			if platform != nil && (platform.OS != operatingSystem || platform.Architecture != architecture) {
				continue
			}
			if ok, err := visit(descriptor.Digest); err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	if ok, err := visit(root); err != nil {
		return nil, err
	} else if ok {
		return proofs, nil
	}
	// Import wrappers may exist only in the runtime's local store. A registry
	// miss remains unqualified; do not invent or reverse-engineer missing bytes.
	if ok, err := visit(target); err != nil {
		return nil, err
	} else if ok {
		return proofs, nil
	}
	return nil, ErrImageProof
}
