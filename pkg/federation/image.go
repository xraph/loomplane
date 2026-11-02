package federation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const (
	MaxImageIndexBytes  = 256 * 1024
	MaxImageIndexes     = 4
	ociImageIndex       = "application/vnd.oci.image.index.v1+json"
	dockerImageIndex    = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociImageManifest    = "application/vnd.oci.image.manifest.v1+json"
	dockerImageManifest = "application/vnd.docker.distribution.manifest.v2+json"
)

type imageIndex struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Manifests     []imageDescriptor `json:"manifests"`
}
type imageDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

// ValidImageIndexes bounds content-addressed proofs before copying or parsing.
// Raw bytes must be retained exactly; JSON normalization changes their digest.
func ValidImageIndexes(indexes [][]byte) bool {
	if len(indexes) > MaxImageIndexes {
		return false
	}
	for _, index := range indexes {
		if len(index) == 0 || len(index) > MaxImageIndexBytes {
			return false
		}
	}
	return true
}

// ResolveImageSelector binds a runtime-reported digest to the deployed reference.
// Repository aliases are permitted only for the same content digest. A different
// digest needs a complete, hash-verified OCI/Docker index chain for the node's
// OS and architecture, or an exclusive runtime wrapper around the pinned image.
// This function performs no network access and accepts no mutable tags.
func ResolveImageSelector(reference, reported, operatingSystem, architecture string, indexes [][]byte) (string, bool) {
	if !ValidPinnedImage(reference) || !ValidPinnedImage(reported) || !ValidImageIndexes(indexes) {
		return "", false
	}
	_, root, _ := strings.Cut(reference, "@")
	_, target, _ := strings.Cut(reported, "@")
	if root == target {
		return reported, true
	}
	if !ValidIdentitySegment(operatingSystem) || !ValidIdentitySegment(architecture) || operatingSystem == "unknown" || architecture == "unknown" {
		return "", false
	}
	proofs := make(map[string][]byte, len(indexes))
	for _, raw := range indexes {
		sum := sha256.Sum256(raw)
		proofs["sha256:"+hex.EncodeToString(sum[:])] = raw
	}
	seen := map[string]bool{}
	var reaches func(string, int) bool
	reaches = func(digest string, depth int) bool {
		if depth >= MaxImageIndexes || seen[digest] {
			return false
		}
		seen[digest] = true
		raw, ok := proofs[digest]
		if !ok {
			return false
		}
		index, valid := parseImageIndex(raw)
		if !valid {
			return false
		}
		for _, descriptor := range index.Manifests {
			if descriptor.Size <= 0 || !ValidPinnedImage("image@"+descriptor.Digest) {
				continue
			}
			platform := descriptor.Platform
			matches := platform != nil && platform.OS == operatingSystem && platform.Architecture == architecture
			switch descriptor.MediaType {
			case ociImageManifest, dockerImageManifest:
				if matches && descriptor.Digest == target {
					return true
				}
			case ociImageIndex, dockerImageIndex:
				if (platform == nil || matches) && reaches(descriptor.Digest, depth+1) {
					return true
				}
			}
		}
		return false
	}
	if !reaches(root, 0) && !exclusiveImageWrapper(proofs, target, root, operatingSystem, architecture) {
		return "", false
	}
	return reported, true
}

func parseImageIndex(raw []byte) (imageIndex, bool) {
	var index imageIndex
	valid := json.Unmarshal(raw, &index) == nil && index.SchemaVersion == 2 && (index.MediaType == ociImageIndex || index.MediaType == dockerImageIndex) && len(index.Manifests) > 0 && len(index.Manifests) <= 128
	return index, valid
}

func exclusiveImageWrapper(proofs map[string][]byte, current, pinned, operatingSystem, architecture string) bool {
	for range MaxImageIndexes {
		index, ok := parseImageIndex(proofs[current])
		if !ok || len(index.Manifests) != 1 {
			return false
		}
		child := index.Manifests[0]
		if child.Size <= 0 || !ValidPinnedImage("image@"+child.Digest) || (child.Platform != nil && (child.Platform.OS != operatingSystem || child.Platform.Architecture != architecture)) {
			return false
		}
		switch child.MediaType {
		case ociImageIndex, dockerImageIndex, ociImageManifest, dockerImageManifest:
			if child.Digest == pinned {
				return true
			}
		default:
			return false
		}
		if child.MediaType != ociImageIndex && child.MediaType != dockerImageIndex {
			return false
		}
		current = child.Digest
	}
	return false
}
