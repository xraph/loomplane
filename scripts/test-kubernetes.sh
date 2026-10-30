#!/usr/bin/env bash
# Installs the Helm chart in a kind cluster and checks Loomplane stays
# available while its pods are deleted, restarted and drained.
#
#   docker build -f deployments/docker/Dockerfile -t loomplane:k8s-test .
#   bash scripts/test-kubernetes.sh loomplane:k8s-test
#
# It makes a kind cluster named loomplane-ha (KIND_CLUSTER to change it) and
# deletes it at the end unless KEEP_CLUSTER=1. It only talks to that
# cluster's context, never your current one.
#
# What it checks:
#   - two API and two worker pods start together without a restart, and seed
#     one authsome app and one SMTP provider between them
#   - exactly one API pod runs the Ctrl Plane scheduler, and another takes
#     over when it goes
#   - a session made on one API pod works on the other
#   - no request through the Service fails while an API pod is deleted, both
#     Deployments restart, and a node is drained
set -euo pipefail

image=${1:?usage: test-kubernetes.sh IMAGE}
cluster=${KIND_CLUSTER:-loomplane-ha}
context=kind-${cluster}
ns=loomplane
release=loomplane
root=$(cd "$(dirname "$0")/.." && pwd)

k() { kubectl --context "$context" -n "$ns" "$@"; }
psql() { k exec deploy/postgres -- psql -U loomplane -d loomplane -tAc "$1"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
step() { printf '\n== %s (%s)\n' "$*" "$(date -u +%T)"; }

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "Something failed; the state of the cluster:" >&2
		k get pods -o wide >&2 || true
		k describe pods -l app.kubernetes.io/instance="$release" 2>/dev/null | tail -n 60 >&2 || true
		k get events --sort-by=.lastTimestamp 2>/dev/null | grep -E "Unhealthy|Killing|Evict" | tail -30 >&2 || true
		k logs -l app.kubernetes.io/component=api --all-containers --prefix --tail=400 2>/dev/null |
			grep -E '"level":"(WARN|ERROR)"' | tail -40 >&2 || true
	fi
	if [ -z "${KEEP_CLUSTER:-}" ]; then
		kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	fi
	exit "$status"
}
trap cleanup EXIT

step "Cluster"
if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
	kind create cluster --name "$cluster" --config "$root/tests/kubernetes/kind.yaml" --wait 180s
fi
kind load docker-image "$image" --name "$cluster"
# Fixture images come from the host too when it can, so a cold cluster
# doesn't wait on registries. kind can't load a multi-platform image the host
# only has one platform of; then the nodes pull it themselves.
for img in postgres:17-alpine adobe/s3mock:5.2.3; do
	docker image inspect "$img" >/dev/null 2>&1 || docker pull -q "$img" >/dev/null || true
	kind load docker-image "$img" --name "$cluster" >/dev/null 2>&1 ||
		echo "The nodes will pull $img themselves."
done
kubectl --context "$context" create namespace "$ns" --dry-run=client -o yaml | kubectl --context "$context" apply -f -

step "PostgreSQL and S3"
k apply -f "$root/tests/kubernetes/fixtures.yaml"
k rollout status deploy/postgres --timeout=600s
k rollout status deploy/s3 --timeout=600s

step "Install the chart"
repo=${image%:*}
tag=${image##*:}
helm --kube-context "$context" upgrade --install "$release" "$root/deployments/helm/loomplane" \
	--namespace "$ns" -f "$root/tests/kubernetes/values.yaml" \
	--set image.repository="$repo" --set image.tag="$tag" \
	--wait --timeout 10m
helm --kube-context "$context" test "$release" -n "$ns" --timeout 2m

step "Replicas came up together without crashing"
api_ready=$(k get deploy "$release" -o jsonpath='{.status.readyReplicas}')
worker_ready=$(k get deploy "$release-worker" -o jsonpath='{.status.readyReplicas}')
[ "$api_ready" = 2 ] || fail "API has $api_ready ready replicas, want 2"
[ "$worker_ready" = 2 ] || fail "worker has $worker_ready ready replicas, want 2"
restarts=$(k get pods -l app.kubernetes.io/instance="$release" -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.restartCount}{"\n"}{end}{end}' | awk '{s+=$1} END {print s+0}')
[ "$restarts" = 0 ] || fail "$restarts container restarts during the first start"
api_nodes=$(k get pods -l app.kubernetes.io/component=api -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u | wc -l | tr -d ' ')
[ "$api_nodes" = 2 ] || fail "API pods share a node; the spread constraints didn't apply"
apps=$(psql "SELECT count(*) FROM authsome_apps")
providers=$(psql "SELECT count(*) FROM herald_providers WHERE name = 'loomplane-smtp'")
[ "$apps" = 1 ] || fail "seeded $apps authsome apps, want 1"
[ "$providers" = 1 ] || fail "seeded $providers SMTP providers, want 1"
echo "2 API and 2 worker pods ready, no restarts, one app and one SMTP provider"

# Postgres keeps a 64-bit advisory lock key as two 32-bit halves.
leader_sql="SELECT count(*) FROM pg_locks, (SELECT hashtextextended('ctrlplane-scheduler', 0) AS key) k
	WHERE locktype = 'advisory' AND granted AND objsubid = 1
	AND classid::bigint = ((k.key >> 32) & 4294967295) AND objid::bigint = (k.key & 4294967295)"
leader_ip_sql="SELECT host(a.client_addr) FROM pg_locks l JOIN pg_stat_activity a USING (pid),
	(SELECT hashtextextended('ctrlplane-scheduler', 0) AS key) k
	WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
	AND l.classid::bigint = ((k.key >> 32) & 4294967295) AND l.objid::bigint = (k.key & 4294967295)"

wait_for_leader() {
	for _ in $(seq 1 60); do
		[ "$(psql "$leader_sql")" = 1 ] && return 0
		sleep 3
	done
	fail "no API pod holds the Ctrl Plane scheduler lock"
}

step "One Ctrl Plane scheduler"
wait_for_leader
leader_ip=$(psql "$leader_ip_sql")
leader_pod=$(k get pods -l app.kubernetes.io/component=api -o jsonpath="{range .items[?(@.status.podIP=='$leader_ip')]}{.metadata.name}{end}")
[ -n "$leader_pod" ] || fail "the lock holder at $leader_ip isn't an API pod"
echo "$leader_pod leads"

step "Probe through the Service"
setup_token=$(k exec deploy/"$release" -- loomplane setup-token --raw | tr -d '\r\n')
[ -n "$setup_token" ] || fail "loomplane setup-token printed nothing"
k delete pod prober --ignore-not-found --wait >/dev/null
# shellcheck disable=SC2016 # The prober's shell expands these, not this one.
# On the control plane with the fixtures, so the drain below never moves the
# pod doing the measuring.
k run prober --image="$image" --image-pull-policy=Never --restart=Never \
	--overrides='{"spec":{"nodeSelector":{"node-role.kubernetes.io/control-plane":""},"tolerations":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]}}' \
	--command -- /bin/sh -c '
	set -u
	setup_token='"$setup_token"'
	base=http://'"$release"':8780
	json="Content-Type: application/json"
	# authsome wants the app'"'"'s publishable key; the chart keeps the default.
	pk="X-Publishable-Key: pk_test_loomplane_dev"
	# A fresh instance takes its first account through setup, which also
	# makes the organization and leaves the session in it. The session
	# comes back as a cookie, and its token works as a bearer token too.
	setup=$(wget -S -O /dev/null --header "$json" --header "$pk" --post-data "{\"name\":\"Ha\",\"email\":\"ha@loomplane.test\",\"password\":\"Correct-horse-battery-9\",\"organization_name\":\"HA\",\"token\":\"$setup_token\"}" $base/api/v1/setup 2>&1) || { echo setup-failed setup; sleep 600; exit 1; }
	token=$(echo "$setup" | sed -n "s/.*[Ss]et-[Cc]ookie: loomplane_session=\([^;]*\).*/\1/p" | head -1)
	[ -n "$token" ] || { echo setup-failed no session cookie; sleep 600; exit 1; }
	auth="Authorization: Bearer $token"
	echo ready
	while true; do
		# The dashboard and an authenticated, database-backed API call: what
		# people actually use.
		if err=$(wget -q -T 5 -O /dev/null $base/ 2>&1); then echo ok; else echo "fail dashboard $(date +%T) $err"; fi
		if err=$(wget -q -T 5 -O /dev/null --header "$pk" --header "$auth" $base/api/v1/session 2>&1); then echo ok; else echo "fail session $(date +%T) $err"; fi
		sleep 0.2
	done'
for _ in $(seq 1 60); do
	logs=$(k logs prober 2>/dev/null || true)
	case "$logs" in *setup-failed*) fail "prober couldn't sign in: $logs" ;; *ready*) break ;; esac
	sleep 2
done
case "$(k logs prober)" in *ready*) ;; *) fail "prober never got ready" ;; esac
sleep 10
ok=$(k logs prober | grep -c '^ok$' || true)
[ "$ok" -ge 20 ] || fail "only $ok requests answered before any disruption"
echo "a session made through the Service works on every pod ($ok requests)"

step "Delete the Ctrl Plane leader"
k delete pod "$leader_pod" --wait=false
sleep 5
wait_for_leader
k rollout status deploy/"$release" --timeout=300s
echo "$(psql "$leader_ip_sql") leads now"

step "Restart both Deployments"
k rollout restart deploy/"$release" deploy/"$release-worker"
k rollout status deploy/"$release" --timeout=300s
k rollout status deploy/"$release-worker" --timeout=300s

step "Drain a node"
node=$(k get pods -l app.kubernetes.io/component=api -o jsonpath='{.items[0].spec.nodeName}')
kubectl --context "$context" drain "$node" --ignore-daemonsets --delete-emptydir-data --timeout=300s
k rollout status deploy/"$release" --timeout=300s
k rollout status deploy/"$release-worker" --timeout=300s
kubectl --context "$context" uncordon "$node"
sleep 10

step "Results"
results=$(k logs prober)
ok=$(echo "$results" | grep -c '^ok$' || true)
failed=$(echo "$results" | grep -c '^fail' || true)
echo "$ok requests answered, $failed failed"
if [ "$failed" -ne 0 ]; then
	echo "$results" | grep '^fail' | head -20 >&2
	fail "requests failed during the disruptions"
fi
[ "$(psql "$leader_sql")" = 1 ] || fail "the scheduler lock isn't held exactly once at the end"
echo "PASS: Loomplane stayed available through every disruption"
