#!/bin/sh
# Pins the model engine images by digest and, with --capabilities, records
# what each engine version accepts:
#
#   scripts/capture-model-engines.sh [--capabilities]
#
# The tags below are picked by hand on the day you run this: the newest
# release of each image that is at least 14 days old. The rule holds for
# the image you pin, not only the release, so the script reads each pinned
# image's build date and fails when one is less than 14 days old. When a
# version changes, rename the files under internal/models/engines/testdata
# and update the releases in internal/models/engines/engines.go to match.
#
# It writes internal/recipes/testdata/model-engine-image-digests.json from
# each tag's multi-arch index, read with an anonymous pull token (no login).
# GPU images (vllm, llamacpp-gpu) keep only their amd64 digest. TEI
# publishes arm64 under its own tag, so its arm64 digest comes from that
# tag's index.
#
# The guard is pinned by index digest as well as tag. Docker's official
# images rebuild caddy:<version> whenever its base image changes, so the
# tag can point at an image a few days old even when the release is weeks
# old. GUARD_INDEX keeps the pin on an image that has passed 14 days; the
# script checks that image still carries Caddy GUARD_TAG and tells you
# when the tag has moved on. Clear it to take whatever the tag points at
# now, and the age check decides whether that image can be pinned yet.
#
# With --capabilities it also runs the amd64 images with Docker (no GPU
# needed) and writes the flag, parser and architecture lists the engines
# package embeds. On an arm64 machine Docker emulates amd64. Set
# CAPTURE_SKIP to a list of image keys to leave out, such as
# CAPTURE_SKIP="vllm llamacpp-gpu" when the 9 GB vLLM image or the CUDA
# image is too big to pull; their lists stay as they are.
#
# tei-<version>-architectures.txt is written by hand from the "Supported
# Models" section of TEI's README at the matching tag.
#
# Needs curl and jq.
set -eu

VLLM_TAG=v0.31.0
TEI_TAG=cpu-1.9.4
TEI_ARM64_TAG=cpu-arm64-1.9.4
LLAMACPP_TAG=server-b11580
LLAMACPP_GPU_TAG=server-cuda-b11580
GUARD_TAG=2.11.7
# caddy:2.11.7 as built on October 5, 2026, before the October 21 rebuild.
GUARD_INDEX=sha256:685ae3d7c34d9b5f8caeddc5d14051db7bbd88ff1f5a2fa0c16bd8ca328fb7af

# MIN_AGE_DAYS is how old every pinned image must be.
MIN_AGE_DAYS=14

VLLM_REPO=docker.io/vllm/vllm-openai
TEI_REPO=ghcr.io/huggingface/text-embeddings-inference
LLAMACPP_REPO=ghcr.io/ggml-org/llama.cpp
GUARD_REPO=docker.io/library/caddy

die() {
	echo "capture-model-engines: $*" >&2
	exit 1
}

capabilities=0
case ${1-} in
"") ;;
--capabilities) capabilities=1 ;;
*) die "usage: scripts/capture-model-engines.sh [--capabilities]" ;;
esac

vllm_version=${VLLM_TAG#v}
tei_version=${TEI_TAG#cpu-}
llamacpp_build=${LLAMACPP_TAG#server-}
[ "$TEI_ARM64_TAG" = "cpu-arm64-$tei_version" ] || die "TEI_ARM64_TAG $TEI_ARM64_TAG doesn't match TEI_TAG $TEI_TAG"
[ "$LLAMACPP_GPU_TAG" = "server-cuda-$llamacpp_build" ] || die "LLAMACPP_GPU_TAG $LLAMACPP_GPU_TAG doesn't match LLAMACPP_TAG $LLAMACPP_TAG"

for tool in curl jq; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

root=$(cd "$(dirname "$0")/.." && pwd)
digests_file=$root/internal/recipes/testdata/model-engine-image-digests.json
testdata=$root/internal/models/engines/testdata
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' INT TERM

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	else
		shasum -a 256 "$1" | cut -d ' ' -f 1
	fi
}

# token REPO prints an anonymous pull token for REPO.
token() {
	case $1 in
	ghcr.io/*) url="https://ghcr.io/token?scope=repository:${1#ghcr.io/}:pull" ;;
	docker.io/*) url="https://auth.docker.io/token?service=registry.docker.io&scope=repository:${1#docker.io/}:pull" ;;
	*) die "no anonymous token endpoint for $1" ;;
	esac
	curl -fsS "$url" | jq -er .token
}

# registry_api REPO prints the registry API base for REPO.
registry_api() {
	case $1 in
	ghcr.io/*) echo "https://ghcr.io/v2/${1#ghcr.io/}" ;;
	docker.io/*) echo "https://registry-1.docker.io/v2/${1#docker.io/}" ;;
	*) die "no registry API for $1" ;;
	esac
}

# fetch_index REPO REF saves the multi-arch index a tag or digest names and
# prints its path. The digest the registry reports must match the bytes it
# sent, and a digest REF must match both.
fetch_index() {
	name=$(echo "$1:$2" | tr '/:' '__')
	body=$work/$name.json
	headers=$work/$name.headers
	curl -fsS -D "$headers" -o "$body" \
		-H "Authorization: Bearer $(token "$1")" \
		-H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
		"$(registry_api "$1")/manifests/$2"
	jq -e '.manifests | type == "array"' "$body" >/dev/null || die "$1:$2 isn't a multi-arch index"
	reported=$(tr -d '\r' <"$headers" | awk 'tolower($1) == "docker-content-digest:" { print $2 }')
	computed=sha256:$(sha256 "$body")
	[ "$reported" = "$computed" ] || die "$1:$2: registry says $reported, body hashes to $computed"
	case $2 in
	sha256:*) [ "$computed" = "$2" ] || die "$1@$2: the registry sent $computed" ;;
	esac
	echo "$body"
}

# image_config REPO DIGEST prints the config of one platform image.
image_config() {
	tok=$(token "$1")
	config=$(curl -fsS -H "Authorization: Bearer $tok" \
		-H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
		"$(registry_api "$1")/manifests/$2" | jq -er .config.digest)
	curl -fsSL -H "Authorization: Bearer $tok" "$(registry_api "$1")/blobs/$config"
}

# check_age REPO DIGEST fails unless the image was built at least
# MIN_AGE_DAYS ago.
check_age() {
	image_config "$1" "$2" >"$work/config.json"
	created=$(jq -er .created "$work/config.json")
	days=$(jq -er '(now - (.created | sub("\\.[0-9]+"; "") | fromdateiso8601)) / 86400 | floor' "$work/config.json")
	[ "$days" -ge "$MIN_AGE_DAYS" ] || die "$1@$2 was built $created, $days days ago; pin an image at least $MIN_AGE_DAYS days old"
	echo "$1@$2 built $created ($days days ago)"
}

# index_digest INDEX_FILE prints the digest of a saved index.
index_digest() {
	echo "sha256:$(sha256 "$1")"
}

# platform_digest INDEX_FILE ARCH prints the one linux/ARCH image in an
# index, skipping attestations and Windows images.
platform_digest() {
	digest=$(jq -r --arg arch "$2" '
		[.manifests[]
			| select(.platform.os == "linux" and .platform.architecture == $arch)
			| select((.platform.variant // "") as $v | $v == "" or $v == "v8")]
		| if length == 1 then .[0].digest else "found \(length)" end' "$1")
	echo "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "$1: want one linux/$2 image, $digest"
	echo "$digest"
}

vllm_index=$(fetch_index "$VLLM_REPO" "$VLLM_TAG")
tei_index=$(fetch_index "$TEI_REPO" "$TEI_TAG")
tei_arm64_index=$(fetch_index "$TEI_REPO" "$TEI_ARM64_TAG")
llamacpp_index=$(fetch_index "$LLAMACPP_REPO" "$LLAMACPP_TAG")
llamacpp_gpu_index=$(fetch_index "$LLAMACPP_REPO" "$LLAMACPP_GPU_TAG")
guard_tag_index=$(fetch_index "$GUARD_REPO" "$GUARD_TAG")
if [ -n "${GUARD_INDEX-}" ]; then
	guard_index=$(fetch_index "$GUARD_REPO" "$GUARD_INDEX")
	moved=$(index_digest "$guard_tag_index")
	[ "$moved" = "$GUARD_INDEX" ] ||
		echo "note: $GUARD_REPO:$GUARD_TAG now points at $moved; keeping the hand-pinned $GUARD_INDEX" >&2
else
	guard_index=$guard_tag_index
fi

vllm_amd64=$(platform_digest "$vllm_index" amd64)
tei_amd64=$(platform_digest "$tei_index" amd64)
tei_arm64=$(platform_digest "$tei_arm64_index" arm64)
llamacpp_amd64=$(platform_digest "$llamacpp_index" amd64)
llamacpp_arm64=$(platform_digest "$llamacpp_index" arm64)
llamacpp_gpu_amd64=$(platform_digest "$llamacpp_gpu_index" amd64)
guard_amd64=$(platform_digest "$guard_index" amd64)
guard_arm64=$(platform_digest "$guard_index" arm64)

for pin in "$guard_amd64" "$guard_arm64"; do
	image_config "$GUARD_REPO" "$pin" | jq -e --arg v "CADDY_VERSION=v$GUARD_TAG" 'any(.config.Env[]; . == $v)' >/dev/null ||
		die "$GUARD_REPO@$pin isn't Caddy $GUARD_TAG"
done

# check_root REPO DIGEST fails unless the image runs as root. The engine
# reads the Hugging Face token from a file owned by root with mode 0400,
# so an engine running as any other user couldn't read it.
check_root() {
	user=$(image_config "$1" "$2" | jq -r '.config.User // ""')
	case $user in
	"" | root | 0 | root:* | 0:*) ;;
	*) die "$1@$2 runs as user \"$user\", not root, so its engine couldn't read the Hugging Face token file (owned by root, mode 0400)" ;;
	esac
}

check_root "$VLLM_REPO" "$vllm_amd64"
check_root "$TEI_REPO" "$tei_amd64"
check_root "$TEI_REPO" "$tei_arm64"
check_root "$LLAMACPP_REPO" "$llamacpp_amd64"
check_root "$LLAMACPP_REPO" "$llamacpp_arm64"
check_root "$LLAMACPP_REPO" "$llamacpp_gpu_amd64"

check_age "$VLLM_REPO" "$vllm_amd64"
check_age "$TEI_REPO" "$tei_amd64"
check_age "$TEI_REPO" "$tei_arm64"
check_age "$LLAMACPP_REPO" "$llamacpp_amd64"
check_age "$LLAMACPP_REPO" "$llamacpp_arm64"
check_age "$LLAMACPP_REPO" "$llamacpp_gpu_amd64"
check_age "$GUARD_REPO" "$guard_amd64"
check_age "$GUARD_REPO" "$guard_arm64"

jq -n \
	--arg captured_at "$(date -u +%Y-%m-%d)" \
	--arg vllm_repo "$VLLM_REPO" --arg vllm_tag "$VLLM_TAG" \
	--arg vllm_index "$(index_digest "$vllm_index")" --arg vllm_amd64 "$vllm_amd64" \
	--arg tei_repo "$TEI_REPO" --arg tei_tag "$TEI_TAG" \
	--arg tei_index "$(index_digest "$tei_index")" --arg tei_amd64 "$tei_amd64" --arg tei_arm64 "$tei_arm64" \
	--arg llamacpp_repo "$LLAMACPP_REPO" --arg llamacpp_tag "$LLAMACPP_TAG" \
	--arg llamacpp_index "$(index_digest "$llamacpp_index")" --arg llamacpp_amd64 "$llamacpp_amd64" --arg llamacpp_arm64 "$llamacpp_arm64" \
	--arg llamacpp_gpu_tag "$LLAMACPP_GPU_TAG" \
	--arg llamacpp_gpu_index "$(index_digest "$llamacpp_gpu_index")" --arg llamacpp_gpu_amd64 "$llamacpp_gpu_amd64" \
	--arg guard_repo "$GUARD_REPO" --arg guard_tag "$GUARD_TAG" \
	--arg guard_index "$(index_digest "$guard_index")" --arg guard_amd64 "$guard_amd64" --arg guard_arm64 "$guard_arm64" \
	'{
		captured_at: $captured_at,
		images: {
			vllm: {repository: $vllm_repo, tag: $vllm_tag, index_digest: $vllm_index, digests: {amd64: $vllm_amd64}},
			tei: {repository: $tei_repo, tag: $tei_tag, index_digest: $tei_index, digests: {amd64: $tei_amd64, arm64: $tei_arm64}},
			llamacpp: {repository: $llamacpp_repo, tag: $llamacpp_tag, index_digest: $llamacpp_index, digests: {amd64: $llamacpp_amd64, arm64: $llamacpp_arm64}},
			"llamacpp-gpu": {repository: $llamacpp_repo, tag: $llamacpp_gpu_tag, index_digest: $llamacpp_gpu_index, digests: {amd64: $llamacpp_gpu_amd64}},
			guard: {repository: $guard_repo, tag: $guard_tag, index_digest: $guard_index, digests: {amd64: $guard_amd64, arm64: $guard_arm64}}
		}
	}' >"$digests_file"
echo "wrote $digests_file"

[ "$capabilities" = 1 ] || exit 0
command -v docker >/dev/null 2>&1 || die "--capabilities needs Docker"
mkdir -p "$testdata"

skipped() {
	case " ${CAPTURE_SKIP-} " in
	*" $1 "*) return 0 ;;
	*) return 1 ;;
	esac
}

# run IMAGE ENTRYPOINT ARGS... runs the amd64 image with no network.
run() {
	image=$1
	entrypoint=$2
	shift 2
	docker run --rm --platform linux/amd64 --network none --entrypoint "$entrypoint" "$image" "$@"
}

# write_list FILE IMAGE writes stdin, sorted and without duplicates, under a
# line naming the image it came from.
write_list() {
	sort -u >"$work/list"
	[ -s "$work/list" ] || die "$1: nothing captured from $2"
	{
		echo "# $2"
		cat "$work/list"
	} >"$1"
	echo "wrote $1"
}

# option_flags prints every long flag on the option lines of a --help page.
option_flags() {
	grep -E '^[[:space:]]*-' | grep -oE -- '--[a-z0-9][a-z0-9-]*' | sed 's/-*$//'
}

# check_shell IMAGE fails unless the image has /bin/sh, which the engine
# wrapper starts the engine with.
check_shell() {
	[ "$(run "$1" /bin/sh -c 'echo ok')" = ok ] || die "$1 has no working /bin/sh"
	echo "checked /bin/sh in $1"
}

vllm_image=$VLLM_REPO@$vllm_amd64
tei_image=$TEI_REPO@$tei_amd64
llamacpp_image=$LLAMACPP_REPO@$llamacpp_amd64
llamacpp_gpu_image=$LLAMACPP_REPO@$llamacpp_gpu_amd64

# The parser and architecture lists are the keys of the registration dicts
# vLLM builds its managers from. Each command also asks the manager itself
# and fails if the two disagree, so a parser registered some other way
# shows up as an error rather than a quiet change to the list.
vllm_tool_parsers='
import vllm.tool_parsers as m
keys = sorted(m._TOOL_PARSERS_TO_REGISTER)
assert keys == sorted(m.ToolParserManager.list_registered()), "ToolParserManager disagrees with _TOOL_PARSERS_TO_REGISTER"
print("\n".join(keys))
'
vllm_reasoning_parsers='
import vllm.reasoning as m
keys = sorted(m._REASONING_PARSERS_TO_REGISTER)
assert keys == sorted(m.ReasoningParserManager.list_registered()), "ReasoningParserManager disagrees with _REASONING_PARSERS_TO_REGISTER"
print("\n".join(keys))
'
vllm_architectures='
from vllm.model_executor.models import registry as m
keys = sorted(m._VLLM_MODELS)
assert keys == sorted(m.ModelRegistry.get_supported_archs()), "ModelRegistry disagrees with _VLLM_MODELS"
print("\n".join(keys))
'

if ! skipped vllm; then
	run "$vllm_image" vllm serve --help=all >"$work/vllm-help" || die "vllm serve --help=all failed"
	option_flags <"$work/vllm-help" | write_list "$testdata/vllm-$vllm_version-flags.txt" "$vllm_image"
	run "$vllm_image" python3 -c "$vllm_tool_parsers" >"$work/tool-parsers" || die "reading vLLM's tool parsers failed"
	write_list "$testdata/vllm-$vllm_version-tool-parsers.txt" "$vllm_image" <"$work/tool-parsers"
	run "$vllm_image" python3 -c "$vllm_reasoning_parsers" >"$work/reasoning-parsers" || die "reading vLLM's reasoning parsers failed"
	write_list "$testdata/vllm-$vllm_version-reasoning-parsers.txt" "$vllm_image" <"$work/reasoning-parsers"
	run "$vllm_image" python3 -c "$vllm_architectures" >"$work/architectures" || die "reading vLLM's architectures failed"
	write_list "$testdata/vllm-$vllm_version-architectures.txt" "$vllm_image" <"$work/architectures"
	check_shell "$vllm_image"
fi

if ! skipped llamacpp; then
	run "$llamacpp_image" /app/llama-server --help >"$work/llamacpp-help" || die "llama-server --help failed"
	option_flags <"$work/llamacpp-help" | write_list "$testdata/llamacpp-$llamacpp_build-flags.txt" "$llamacpp_image"
	check_shell "$llamacpp_image"
fi

if ! skipped llamacpp-gpu; then
	check_shell "$llamacpp_gpu_image"
fi

if ! skipped tei; then
	check_shell "$tei_image"
fi
