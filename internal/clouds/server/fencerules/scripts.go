package fencerules

// IngressAdminScript is the fixed script that only talks to the container's loopback admin. A non-detached
// exec start stays inside the daemon fence until wget exits and readback ends.
const IngressAdminScript = `set -eu
d=$(mktemp -d)
trap 'rm -rf "$d"' EXIT
if [ "$1" = apply ]; then
 printf '%s' "$3" | base64 -d > "$d/new.json"
 wget -q -T 20 --header='Content-Type: application/json' --header="If-Match: $2" --post-file="$d/new.json" -O "$d/result" http://127.0.0.1:2019/config/
fi
if [ "$1" = reload ]; then
 printf '%s' "$3" | base64 -d > "$d/new.json"
 wget -q -T 20 --header='Content-Type: application/json' --header='Cache-Control: must-revalidate' --header="If-Match: $2" --post-file="$d/new.json" -O "$d/result" http://127.0.0.1:2019/config/
fi
wget -S -T 20 -O "$d/config" http://127.0.0.1:2019/config/ 2> "$d/headers"
awk 'tolower($1)=="etag:" {sub(/^[^:]*:[ \t]*/, ""); sub(/\r$/, ""); print}' "$d/headers"
cat "$d/config"
`

// IngressProbeScript fetches the proxy's backend with a Host header.
const IngressProbeScript = `wget -q -S -T 20 --header="Host: $1" -O /dev/null http://127.0.0.1:8081/ 2>&1 || true`

// IngressTokenScript removes one token file; the fence checks the zone ID.
const IngressTokenScript = `rm -f /config/dns/"$1".token` //nolint:gosec // G101: a shell command naming a token file, not a credential

// LegacyIngressImage pins the multi-platform Caddy 2.11.7 Alpine release.
// It has no DNS provider modules, so it can't answer DNS-01 challenges.
const LegacyIngressImage = "caddy:2.11.7-alpine@sha256:d8542f48d34a9cf4e4c11a478865229840e87e4c96ea3f439101f31a5d35f75f"

// IngressImage pins the DNS-capable proxy by its multi-platform digest once
// it is published. Until then it is empty.
const IngressImage = ""

// DNSImage is IngressImage. Tests stand an image in by assigning it.
var DNSImage = IngressImage

// IngressImages are the images an owned proxy may run.
func IngressImages() []string {
	if DNSImage != "" {
		return []string{LegacyIngressImage, DNSImage}
	}
	return []string{LegacyIngressImage}
}
