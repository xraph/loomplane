package recipes

import (
	"fmt"
	"strings"
)

func (r *Recipe) Cache() bool { return r.Engine() == "redis" || r.Engine() == "valkey" }

func cacheRecipe(kind, version, architecture string) (*Recipe, error) {
	var product, repository string
	var digests map[string]string
	switch {
	case kind == "redis" && version == "8.2":
		product, repository = "8.2.10", "docker.io/library/redis"
		digests = map[string]string{"amd64": "sha256:c4206b4e79e4bcc8b30679eeece996d98b27ffb1f0a15fd567338ed4d0880258", "arm64": "sha256:0f16771e42330c740f93cc57e2ebd1d60c75569b6e4e2aa90ff4bc8f65618078"}
	case kind == "valkey" && version == "8.1":
		product, repository = "8.1.10", "docker.io/valkey/valkey"
		digests = map[string]string{"amd64": "sha256:32627109abf6f741121096b45c732f758876803efd7b2e1018ebc0350d117119", "arm64": "sha256:6bd0360157519e3de7c49f6bae7d2bdfab574083bd7d266182db36b1646b0936"}
	default:
		return nil, fmt.Errorf("%s %s has no supported recipe", kind, version)
	}
	digest, ok := digests[architecture]
	if !ok {
		return nil, fmt.Errorf("%s recipe requires a checked amd64 or arm64 target, got %q", kind, architecture)
	}
	return &Recipe{ID: kind + "-" + version, Revision: "1", ProductVersion: product, Image: repository + "@" + digest, Architecture: architecture, MountPath: "/data", BackupFormat: kind + "-rdb-v1"}, nil
}

// Credentials are hashed into an ephemeral config, never argv or the data volume.
// A recovery copy loads only its checked RDB; an empty AOF must not override it.
func (r *Recipe) cacheArgs(recovery bool) []string {
	aof := "yes"
	save, appRules := "60 1000", "+@all -@admin -@dangerous"
	if recovery {
		aof = "no"
		save, appRules = `""`, "+@read +@connection -@admin -@dangerous"
	}
	script := strings.NewReplacer("{ENGINE}", r.Engine(), "{AOF}", aof, "{SAVE}", save, "{APP_RULES}", appRules).Replace(`set -eu
: "${LOOMPLANE_CACHE_ADMIN_PASSWORD:?}" "${LOOMPLANE_APP_PASSWORD:?}"
admin=$(printf '%s' "$LOOMPLANE_CACHE_ADMIN_PASSWORD" | sha256sum); admin=${admin%% *}
app=$(printf '%s' "$LOOMPLANE_APP_PASSWORD" | sha256sum); app=${app%% *}
umask 077
cat > /tmp/loomplane-cache.conf <<EOF
bind 0.0.0.0
port 6379
protected-mode yes
databases 1
dir /data
dbfilename dump.rdb
appendonly {AOF}
appendfsync everysec
save {SAVE}
maxmemory 128mb
maxmemory-policy noeviction
user default on #${admin} ~* &* +@all
user app on #${app} ~* &* {APP_RULES}
EOF
chmod 644 /tmp/loomplane-cache.conf
exec docker-entrypoint.sh {ENGINE}-server /tmp/loomplane-cache.conf`)
	return []string{"sh", "-c", script}
}
