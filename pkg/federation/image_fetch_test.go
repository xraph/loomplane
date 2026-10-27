package federation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCollectImageIndexesUsesPinnedRepositoryAndRawProofs(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	leaf := imageIndexFixture(digest, ociImageManifest)
	_, child, _ := strings.Cut(indexReference(leaf), "@")
	root := imageIndexFixture(child, ociImageIndex)
	reference := indexReference(root)
	calls := []string{}
	fetch := func(_ context.Context, ref string) ([]byte, error) {
		calls = append(calls, ref)
		switch ref {
		case reference:
			return root, nil
		case "registry.example/api@" + child:
			return leaf, nil
		default:
			t.Error("unexpected manifest fetch", ref)
			return nil, errors.New("missing")
		}
	}
	proofs, err := CollectImageIndexes(t.Context(), reference, "attacker.example/import@"+digest, "linux", "arm64", fetch)
	if err != nil || len(proofs) != 2 || len(calls) != 2 || string(proofs[0]) != string(root) || string(proofs[1]) != string(leaf) {
		t.Fatal("proof collection failed", calls, err)
	}
	root[0] = '!'
	if proofs[0][0] == '!' {
		t.Fatal("proof aliases fetch buffer")
	}
}

func TestCollectImageIndexesBoundsAndDenials(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	raw := imageIndexFixture(digest, ociImageManifest)
	reference := indexReference(raw)
	for _, scenario := range []string{"changed bytes", "oversized", "wrong platform", "foreign digest", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			arch, reported := "arm64", "runtime.example/api@"+digest
			calls := 0
			if scenario == "wrong platform" {
				arch = "amd64"
			}
			if scenario == "foreign digest" {
				reported = "runtime.example/api@sha256:" + strings.Repeat("b", 64)
			}
			if scenario == "cancelled" {
				cancel()
			}
			_, err := CollectImageIndexes(ctx, reference, reported, "linux", arch, func(_ context.Context, ref string) ([]byte, error) {
				calls++
				if !strings.HasPrefix(ref, "registry.example/api@") {
					t.Error("followed runtime repository", ref)
				}
				switch scenario {
				case "changed bytes":
					return append(append([]byte{}, raw...), ' '), nil
				case "oversized":
					return make([]byte, MaxImageIndexBytes+1), nil
				}
				return raw, nil
			})
			if err == nil || calls > MaxImageIndexes || (scenario == "cancelled" && calls != 0) {
				t.Fatal("untrusted proof accepted", err, calls)
			}
		})
	}
	calls := 0
	proofs, err := CollectImageIndexes(t.Context(), "registry.example/api@"+digest, "alias.example/api@"+digest, "", "", func(context.Context, string) ([]byte, error) { calls++; return nil, nil })
	if err != nil || len(proofs) != 0 || calls != 0 {
		t.Fatal("identical digest fetched content", err)
	}
	// A deep graph cannot turn a bounded attestation check into unlimited I/O.
	blobs := map[string][]byte{}
	next := digest
	for range MaxImageIndexes + 2 {
		body := imageIndexFixture(next, ociImageIndex)
		ref := indexReference(body)
		blobs[ref] = body
		_, next, _ = strings.Cut(ref, "@")
	}
	calls = 0
	_, err = CollectImageIndexes(t.Context(), "registry.example/api@"+next, "runtime.example/api@"+digest, "linux", "arm64", func(_ context.Context, ref string) ([]byte, error) { calls++; return blobs[ref], nil })
	if err == nil || calls > MaxImageIndexes {
		t.Fatal("unbounded proof graph", calls, err)
	}
}

func TestCollectImageIndexesFindsExclusiveRuntimeWrapper(t *testing.T) {
	pinned := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	reference := indexReference(pinned)
	_, digest, _ := strings.Cut(reference, "@")
	wrapper := imageIndexFixture(digest, ociImageManifest)
	_, wrapped, _ := strings.Cut(indexReference(wrapper), "@")
	proofs, err := CollectImageIndexes(t.Context(), reference, "foreign.example/import@"+wrapped, "linux", "arm64", func(_ context.Context, ref string) ([]byte, error) {
		if ref == reference {
			return pinned, nil
		}
		if ref == "registry.example/api@"+wrapped {
			return wrapper, nil
		}
		return nil, errors.New("missing wrapper")
	})
	if err != nil || len(proofs) != 1 || string(proofs[0]) != string(wrapper) {
		t.Fatal("exclusive wrapper not proved", err)
	}
}
