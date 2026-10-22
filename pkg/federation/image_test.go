package federation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func imageIndexFixture(digest, media string) []byte {
	return []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":%q,"digest":%q,"size":1234,"platform":{"os":"linux","architecture":%q}}]}`, media, digest, "arm64"))
}
func indexReference(body []byte) string {
	sum := sha256.Sum256(body)
	return "registry.example/api@sha256:" + hex.EncodeToString(sum[:])
}
func TestImageSelectorVerifiesPlatformManifestAgainstPinnedIndex(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	reported := "runtime.example/import@" + digest
	index := imageIndexFixture(digest, "application/vnd.oci.image.manifest.v1+json")
	for name, input := range map[string]struct {
		reference, reported, arch string
		proofs                    [][]byte
		want                      bool
	}{
		"same digest repository alias": {"registry.example/api@" + digest, reported, "arm64", nil, true},
		"proved platform":              {indexReference(index), reported, "arm64", [][]byte{index}, true},
		"missing proof":                {indexReference(index), reported, "arm64", nil, false},
		"wrong platform":               {indexReference(index), reported, "amd64", [][]byte{index}, false},
		"wrong manifest":               {indexReference(index), "runtime.example/import@sha256:" + strings.Repeat("b", 64), "arm64", [][]byte{index}, false},
		"changed index":                {indexReference(index), reported, "arm64", [][]byte{append(append([]byte(nil), index...), ' ')}, false},
		"mutable reported tag":         {indexReference(index), "runtime.example/api:latest", "arm64", [][]byte{index}, false},
		"unrelated root":               {"registry.example/api@sha256:" + strings.Repeat("c", 64), reported, "arm64", [][]byte{index}, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := ResolveImageSelector(input.reference, input.reported, "linux", input.arch, input.proofs)
			if ok != input.want || (ok && got != input.reported) {
				t.Fatalf("selector=%q valid=%v", got, ok)
			}
		})
	}
	for _, media := range []string{"application/vnd.oci.image.config.v1+json", "", "application/vnd.oci.artifact.manifest.v1+json"} {
		proof := imageIndexFixture(digest, media)
		if _, ok := ResolveImageSelector(indexReference(proof), reported, "linux", "arm64", [][]byte{proof}); ok {
			t.Fatal("non-image descriptor accepted")
		}
	}
}
func TestImageSelectorBoundsAndNestedIndexes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	leaf := imageIndexFixture(digest, "application/vnd.oci.image.manifest.v1+json")
	_, childDigest, _ := strings.Cut(indexReference(leaf), "@")
	root := imageIndexFixture(childDigest, "application/vnd.oci.image.index.v1+json")
	reported := "runtime.example/import@" + digest
	if _, ok := ResolveImageSelector(indexReference(root), reported, "linux", "arm64", [][]byte{root, leaf}); !ok {
		t.Fatal("valid nested manifest proof denied")
	}
	if _, ok := ResolveImageSelector(indexReference(root), reported, "linux", "arm64", [][]byte{root}); ok {
		t.Fatal("missing intermediate manifest accepted")
	}
	for _, proofs := range [][][]byte{{root, leaf, root, leaf, root}, {make([]byte, 256*1024+1)}} {
		if _, ok := ResolveImageSelector(indexReference(root), reported, "linux", "arm64", proofs); ok {
			t.Fatal("unbounded manifest proof accepted")
		}
	}
}

func TestImageSelectorRequiresExclusiveRuntimeWrapper(t *testing.T) {
	pinned := "sha256:" + strings.Repeat("a", 64)
	wrap := func(child string, extra bool) []byte {
		descriptor := fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.index.v1+json","digest":%q,"size":1234}`, child)
		if extra {
			descriptor += "," + descriptor
		}
		return []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` + descriptor + `]}`)
	}
	valid := wrap(pinned, false)
	ambiguous := wrap(pinned, true)
	foreign := wrap("sha256:"+strings.Repeat("b", 64), false)
	_, child, _ := strings.Cut(indexReference(valid), "@")
	nested := wrap(child, false)
	for name, input := range map[string]struct {
		runtime []byte
		proofs  [][]byte
		want    bool
	}{
		"exclusive wrapper":         {valid, [][]byte{valid}, true},
		"nested exclusive wrappers": {nested, [][]byte{nested, valid}, true},
		"missing wrapper":           {valid, nil, false},
		"changed bytes":             {valid, [][]byte{append(append([]byte(nil), valid...), ' ')}, false},
		"additional descriptor":     {ambiguous, [][]byte{ambiguous}, false},
		"unrelated child":           {foreign, [][]byte{foreign}, false},
		"missing intermediate":      {nested, [][]byte{nested}, false},
	} {
		t.Run(name, func(t *testing.T) {
			reported := indexReference(input.runtime)
			got, ok := ResolveImageSelector("registry.example/api@"+pinned, reported, "linux", "arm64", input.proofs)
			if ok != input.want || (ok && got != reported) {
				t.Fatalf("selector=%q valid=%v", got, ok)
			}
		})
	}
}
