// Controlled HTTP contracts for the full dashboard. No infrastructure effects.
// Auth and organization session requests pass to the isolated real API.
import http from "node:http";
import { readFileSync } from "node:fs";

const stamp = "2026-10-22T12:00:00Z";
const org = process.env.SHARED_CLOUD_FIXTURE_ORG;
if (!org) throw new Error("SHARED_CLOUD_FIXTURE_ORG is required");
const upstream = new URL(
  process.env.SHARED_CLOUD_FIXTURE_API ?? "http://127.0.0.1:18880",
);
const port = Number(process.env.SHARED_CLOUD_FIXTURE_PORT ?? 18881);
let counter = 1;
let stale = false;
let routeFailure = true;
const cloud = {
  id: "cloud_fixture",
  org_id: org,
  project_id: "fixture_web",
  name: "Shared Cloud fixture",
  default_placement: null,
  revision: 4,
  created_at: stamp,
  updated_at: stamp,
};
const projects = [
  {
    id: "fixture_web",
    org_id: org,
    name: "Web app",
    repositories: [],
    slug: "web-app",
    status: "ready",
    description: "",
    created_by: "fixture",
    icon: "globe",
    color: "blue",
    created_at: stamp,
    updated_at: stamp,
  },
  {
    id: "fixture_jobs",
    org_id: org,
    name: "Jobs",
    repositories: [],
    slug: "jobs",
    status: "ready",
    description: "",
    created_by: "fixture",
    icon: "box",
    color: "green",
    created_at: stamp,
    updated_at: stamp,
  },
];
const accounts = [
  {
    id: "fixture_compute",
    cloud: "fixture-provider",
    name: "Compute account",
    account: "compute",
    status: "ready",
    generation: 3,
  },
  {
    id: "fixture_dns",
    cloud: "fixture-provider",
    name: "Independent DNS account",
    account: "dns",
    status: "ready",
    generation: 9,
  },
  {
    id: "fixture_ssh",
    cloud: "server",
    name: "Pinned SSH account",
    account: "shared-host",
    status: "ready",
    generation: 2,
  },
].map((c) => ({
  ...c,
  org_id: org,
  created_at: stamp,
  updated_at: stamp,
  checked_at: stamp,
  created_by: "fixture",
  message: "",
  missing: [],
  details: {},
}));
const providers = [
  {
    name: "fixture-provider",
    cloud: "fixture-provider",
    title: "Fixture infrastructure",
    available: true,
    model: "managed",
    description: "Controlled provider contracts",
    runs: [
      { workload: "service", execution: "container", creates: "deployment" },
    ],
    infrastructure: [
      "server:provision",
      "server:resize",
      "server:delete",
      "dns:zones:read",
      "dns:records:read",
      "dns:records:upsert",
      "dns:records:delete",
    ],
  },
];
const health = {
  effective_cpu_millis: 4000,
  effective_memory_bytes: 8589934592,
  cpus: 4,
  memory_bytes: 8589934592,
  checked_at: stamp,
  docker_id: "fixture-daemon",
  architecture: "amd64",
};
const servers = ["a", "b"].map((name, i) => ({
  id: `fixture_host_${name}`,
  org_id: org,
  cloud_id: cloud.id,
  name: `shared-host-${name}`,
  connection_id: i ? "fixture_ssh" : "fixture_compute",
  runtime_connection_id: "fixture_ssh",
  account_id: "compute",
  resource_id: `external-${name}`,
  provider: i ? "server" : "fixture-provider",
  ownership: i ? "adopted" : "managed",
  size: "small",
  status: "ready",
  revision: 1,
  state_revision: 1,
  health,
  region: "us-east",
  draining: false,
  creation_operation_id: "",
  created_at: stamp,
  updated_at: stamp,
}));
const targets = [
  {
    id: "fixture_frontend",
    org_id: org,
    project_id: "fixture_web",
    cloud_id: cloud.id,
    name: "frontend",
    display_name: "Frontend",
    provider: "fixture-provider",
    connection_id: "fixture_compute",
    runtime: "managed",
    region: "us-east",
    tags: ["web"],
    revision: 1,
    created_at: stamp,
    updated_at: stamp,
  },
];
const grants = [
  {
    cloud_id: cloud.id,
    project_id: "fixture_web",
    org_id: org,
    target_ids: [],
    revision: 1,
    revoked: false,
  },
];
const pools = [];
const operations = [];
const zones = [];
const routes = [];
const routeOperations = [];
const syncs = ["restore_retained", "recreate", "mixed"].map((mode, i) => ({
  id: `fixture_sync_${i}`,
  cloud_id: cloud.id,
  org_id: org,
  project_id: "fixture_web",
  preview_id: "fixture_preview",
  created_by: "fixture",
  input: { cloud_id: cloud.id, revision: 1 },
  status: "routing_pending",
  attempt: 1,
  before: targets[0],
  after: {
    ...targets[0],
    runtime: "server",
    pool_id: "fixture_pool",
    provider: "server",
    connection_id: "",
  },
  environments: [
    {
      project_id: "fixture_web",
      environment_id: `fixture_env_${i}`,
      environment: "production",
      status: "routing_pending",
      services: ["api"],
      summary: {
        create: 0,
        replace: 1,
        update: 0,
        delete: 0,
        adopt: 0,
        keep: 0,
      },
      external_routing: false,
      compensation_mode: mode,
      compensation_message:
        mode === "restore_retained"
          ? "Restore the exact retained server runtime."
          : mode === "recreate"
            ? "Recreate managed services from saved inputs with a new runtime identity."
            : "Server restore and managed recreation are reviewed separately. Unresolved managed effects require reconciliation.",
      routing_status: "routing_pending",
      routes: [
        {
          route_id: "fixture_route",
          hostname: "api.example.test",
          status: "waiting",
          message: "Keep the previous host until DNS TTL and drain complete.",
          record_type: "A",
          value: "192.0.2.20",
          ttl: 300,
          not_before: "2027-01-01T00:00:00Z",
        },
      ],
    },
  ],
  created_at: stamp,
  updated_at: stamp,
}));
const previews = [];
if (process.env.SHARED_CLOUD_FIXTURE_STATE) {
  const saved = JSON.parse(
    readFileSync(process.env.SHARED_CLOUD_FIXTURE_STATE, "utf8"),
  );
  stale = saved.stale ?? false;
  routeFailure = saved.routeFailure ?? true;
  Object.assign(cloud, saved.cloud);
  for (const [key, list] of Object.entries({
    servers,
    pools,
    operations,
    targets,
    grants,
    zones,
    syncs,
    routes,
    routeOperations,
  }))
    list.splice(0, list.length, ...(saved[key] ?? []));
  for (const sync of syncs)
    if (sync.status === "succeeded")
      for (const environment of sync.environments) {
        environment.routing_status = "succeeded";
        for (const route of environment.routes ?? []) {
          route.status = "succeeded";
          route.message = "Routing and retained cleanup verified.";
          route.not_before = "0001-01-01T00:00:00Z";
        }
      }
  counter = Date.now();
}
const bodyOf = (req) =>
  new Promise((resolveBody, reject) => {
    let data = "";
    req.on("data", (c) => {
      data += c;
      if (data.length > 1e6) reject(new Error("body too large"));
    });
    req.on("end", () => resolveBody(data ? JSON.parse(data) : {}));
    req.on("error", reject);
  });
const send = (res, body, status = 200) => {
  res.writeHead(status, {
    "content-type": "application/json",
    "cache-control": "no-store",
  });
  res.end(JSON.stringify(body));
};
const error = (res, message, status = 409) => send(res, { message }, status);
const id = (prefix) => `${prefix}_${counter++}`;
function usage(server) {
  if (server.id !== "fixture_host_a")
    return {
      server_id: server.id,
      draining: server.draining,
      reserved_cpu_millis: 0,
      reserved_memory_bytes: 0,
      workloads: [],
      retained_storage: [],
    };
  return {
    server_id: server.id,
    draining: server.draining,
    reserved_cpu_millis: 500,
    reserved_memory_bytes: 268435456,
    workloads: [
      {
        project_id: "fixture_web",
        environment_id: "fixture_production",
        service: "api",
        phase: "bound",
        active: true,
        binding_id: "fixture_binding",
        plan_id: "fixture_plan",
        pool_id: "fixture_pool",
        execution_revision: 1,
        cpu_millis: 500,
        memory_bytes: 268435456,
      },
    ],
    retained_storage: [
      {
        project_id: "fixture_jobs",
        environment_id: "fixture_jobs_env",
        service: "worker",
        plan_id: "fixture_old_plan",
      },
    ],
  };
}
function progress(op) {
  op.attempt++;
  op.status = routeFailure ? "waiting" : "succeeded";
  op.message = routeFailure
    ? "DNS verified. Certificate issuance pending. Resolve issuance and retry the saved operation."
    : "DNS, TLS and backend verified.";
  op.route.status = routeFailure ? "waiting" : "ready";
  op.route.verification = {
    dns: true,
    tls: !routeFailure,
    backend: !routeFailure,
    checked_at: stamp,
    cert_expires: "2027-01-01T00:00:00Z",
    ...(routeFailure ? { tls_error: "Certificate issuance pending" } : {}),
  };
  const persisted = routes.find((route) => route.id === op.route.id);
  if (persisted) Object.assign(persisted, structuredClone(op.route));
  return op;
}
const server = http.createServer(async (req, res) => {
  try {
    const path = new URL(req.url, "http://fixture").pathname;
    if (path === "/__fixture") {
      if (req.method === "POST") {
        const inBody = await bodyOf(req);
        stale = !!inBody.stale;
        routeFailure = inBody.routeFailure ?? routeFailure;
      }
      return send(res, {
        controlled_backend: true,
        cloud_id: cloud.id,
        stale,
        routeFailure,
        operations: operations.length,
      });
    }
    if (!path.startsWith("/api/v1/") || path === "/api/v1/session")
      return proxy(req, res);
    const method = req.method;
    const body = method === "GET" ? {} : await bodyOf(req);
    if (path === "/api/v1/providers") return send(res, { providers });
    if (path === "/api/v1/connections")
      return send(res, { connections: accounts });
    if (path === "/api/v1/projects") return send(res, { projects });
    if (/\/projects\/[^/]+\/environments$/.test(path))
      return send(res, {
        environments: [
          {
            id: "fixture_production",
            project_id: path.split("/")[4],
            org_id: org,
            name: "production",
            kind: "production",
            created_at: stamp,
            updated_at: stamp,
          },
        ],
      });
    if (path === "/api/v1/clouds" || /\/projects\/[^/]+\/clouds$/.test(path))
      return send(res, { clouds: [cloud] });
    if (path === "/api/v1/clouds/syncs" || path.endsWith("/cloud-syncs"))
      return send(res, { syncs });
    if (path === `/api/v1/clouds/${cloud.id}`) return send(res, cloud);
    if (/\/environments\/[^/]+\/cloud$/.test(path))
      return send(res, { assignment: null, revision: 0 });
    if (path === "/api/v1/clouds/changes/preview") {
      const before = targets.find((target) => target.id === body.target_id);
      const preview = {
        id: id("preview"),
        org_id: org,
        project_id: "",
        revision: id("review_digest"),
        input: structuredClone(body),
        created_at: stamp,
        blockers: [],
        environments: [
          {
            ...structuredClone(syncs[0].environments[0]),
            compensation_mode: undefined,
            compensation_message: undefined,
            status: "pending",
          },
        ],
      };
      if (body.default) {
        preview.before_default = structuredClone(cloud.default_placement);
        preview.after_default = body.provider
          ? {
              provider: body.provider,
              connection_id: body.connection_id,
              region: body.region,
            }
          : null;
      } else {
        if (!before) return error(res, "Target missing", 404);
        preview.before = structuredClone(before);
        preview.after = {
          ...before,
          ...body,
          id: before.id,
          revision: before.revision + 1,
        };
      }
      previews.push(preview);
      return send(res, preview);
    }
    if (path === "/api/v1/clouds/changes/apply") {
      const preview = previews.find(
        (preview) => preview.id === body.preview_id,
      );
      if (!preview || preview.revision !== body.revision)
        return error(res, "Saved review required");
      if (stale) {
        stale = false;
        return error(res, "Placement changed; refresh the review");
      }
      if (preview.input.default) {
        if (cloud.revision !== preview.input.revision)
          return error(res, "Cloud changed; refresh review");
        cloud.default_placement = preview.after_default;
        cloud.revision++;
      } else {
        const target = targets.find(
          (target) => target.id === preview.input.target_id,
        );
        if (target.revision !== preview.input.revision)
          return error(res, "Target changed; refresh review");
        Object.assign(target, preview.after);
      }
      const sync = {
        ...structuredClone(syncs[0]),
        id: id("sync"),
        preview_id: preview.id,
        input: preview.input,
        before: preview.before,
        after: preview.after,
        before_default: preview.before_default,
        after_default: preview.after_default,
        default: preview.input.default,
        environments: preview.environments,
        status: "routing_pending",
        attempt: 1,
      };
      syncs.unshift(sync);
      return send(res, sync, 202);
    }
    if (/\/clouds\/syncs\/[^/]+(?:\/retry)?$/.test(path)) {
      const sync = syncs.find((sync) => sync.id === path.split("/")[5]);
      if (!sync) return error(res, "Sync missing", 404);
      if (path.endsWith("/retry")) {
        sync.status = "succeeded";
        sync.attempt++;
        for (const environment of sync.environments) {
          environment.status = "succeeded";
          environment.routing_status = "succeeded";
          for (const route of environment.routes ?? []) {
            route.status = "succeeded";
            route.message = "Routing and retained cleanup verified.";
            route.not_before = "0001-01-01T00:00:00Z";
          }
        }
      }
      return send(res, sync);
    }
    if (/\/targets\/[^/]+$/.test(path)) {
      const target = targets.find(
        (target) => target.id === path.split("/").at(-1),
      );
      if (!target) return error(res, "Target missing", 404);
      if (method === "PUT") {
        if (target.revision !== body.revision)
          return error(res, "Target changed; refresh");
        Object.assign(target, {
          display_name: body.display_name,
          tags: body.tags,
          revision: target.revision + 1,
        });
      }
      return send(res, target);
    }
    if (path.endsWith("/default-placement")) {
      if (body.revision !== cloud.revision)
        return error(res, "Cloud changed; refresh review");
      cloud.default_placement = body.default_placement;
      cloud.revision++;
      return send(res, cloud);
    }
    if (path.endsWith("/targets")) {
      if (method === "POST") {
        const target = {
          ...body,
          id: id("target"),
          cloud_id: cloud.id,
          org_id: org,
          project_id: "",
          revision: 1,
          created_at: stamp,
          updated_at: stamp,
        };
        targets.push(target);
        return send(res, target, 201);
      }
      return send(res, { targets });
    }
    if (path.endsWith("/projects") && path.includes("/clouds/"))
      return send(res, { grants });
    if (/\/clouds\/[^/]+\/projects\/[^/]+$/.test(path)) {
      const projectId = path.split("/").at(-1);
      const current = grants.find((g) => g.project_id === projectId);
      if (method === "DELETE") {
        if (current) current.revoked = true;
        return send(res, {});
      }
      const next = {
        cloud_id: cloud.id,
        project_id: projectId,
        target_ids: body.target_ids,
        revision: (current?.revision ?? 0) + 1,
        revoked: false,
      };
      if (current) Object.assign(current, next);
      else grants.push(next);
      return send(res, next);
    }
    if (path.endsWith("/servers"))
      return send(res, {
        servers: servers.filter(
          (server) => !["deleted", "disconnected"].includes(server.status),
        ),
      });
    if (path.includes("/server-options/"))
      return send(res, {
        regions: [{ id: "us-east", name: "US East" }],
        images: [
          {
            id: "linux",
            name: "Linux with bootstrap",
            bootstrap: true,
            regions: ["us-east"],
          },
        ],
        sizes: ["small", "large"].map((s, i) => ({
          id: s,
          cpus: 2 + i * 2,
          memory_mib: 4096 + i * 4096,
          disk_gb: 50,
          hourly_usd: 0.04 + i * 0.04,
          monthly_usd: 30 + i * 30,
          regions: ["us-east"],
        })),
      });
    if (path.endsWith("/servers/preview")) {
      if (["delete", "disconnect"].includes(body.kind)) {
        const host = servers.find((server) => server.id === body.server_id);
        if (
          host &&
          (usage(host).workloads.length || usage(host).retained_storage.length)
        )
          return error(
            res,
            "Server has active workloads or retained storage. Review drain, moves and cleanup before removal.",
          );
      }
      const op = {
        id: id("operation"),
        org_id: org,
        cloud_id: cloud.id,
        server_id: body.server_id || id("server"),
        input: body,
        status: "preview",
        review: id("immutable_review"),
        account_id: "compute",
        connection_id: body.connection_id,
        connection_generation: 3,
        ownership:
          body.kind === "adopt" || body.kind === "disconnect"
            ? "adopted"
            : "managed",
        state_revision: 1,
        attempt: 0,
        attempted: false,
        enrolled: false,
        estimated_hourly_usd: 0.04,
        actions: [],
        public_ipv4: [],
        message: "Review pinned account and ownership",
        expires_at: "2027-01-01T00:00:00Z",
        created_at: stamp,
        updated_at: stamp,
      };
      operations.unshift(op);
      return send(res, op);
    }
    if (/\/infrastructure-operations$/.test(path))
      return send(res, { operations });
    if (path.includes("/infrastructure-operations/")) {
      const pieces = path.split("/");
      const op = operations.find((o) => o.id === pieces[6]);
      if (!op) return error(res, "Operation missing", 404);
      const action = pieces[7];
      if (action === "cancel") op.status = "cancelled";
      if (action === "apply" || action === "retry") {
        if (stale) {
          stale = false;
          return error(res, "Infrastructure changed; review again");
        }
        if (action === "apply" && body.review !== op.review)
          return error(res, "Saved review required");
        op.attempt++;
        op.attempted = true;
        op.status = action === "retry" ? "succeeded" : "waiting";
        op.message =
          op.status === "waiting"
            ? "Waiting for verified enrollment. Retry only after reconciliation."
            : "Enrollment and runtime health verified";
        if (op.status === "succeeded") {
          const existing = servers.find((s) => s.id === op.server_id);
          if (["delete", "disconnect"].includes(op.input.kind)) {
            if (existing)
              existing.status =
                op.input.kind === "delete" ? "deleted" : "disconnected";
          } else if (existing) {
            existing.size = op.input.size;
            existing.revision++;
          } else
            servers.push({
              ...servers[0],
              id: op.server_id,
              name: op.input.name,
              ownership: op.ownership,
              connection_id: op.connection_id,
              status: "ready",
              draining: false,
            });
        }
      }
      return send(res, op);
    }
    if (path.includes("/servers/")) {
      const pieces = path.split("/");
      const host = servers.find((s) => s.id === pieces[6]);
      if (!host) return error(res, "Server missing", 404);
      if (path.endsWith("/usage")) return send(res, usage(host));
      if (path.endsWith("/drain/preview"))
        return send(res, {
          review: `drain-${host.revision}`,
          server_id: host.id,
          server_revision: host.revision,
          reservations: 1,
          usage: usage(host),
        });
      if (path.endsWith("/drain/apply")) {
        if (body.review !== `drain-${host.revision}`)
          return error(res, "Drain changed; review again");
        host.draining = true;
        host.revision++;
      }
      return send(res, host);
    }
    if (path.endsWith("/pools") || /\/pools\/[^/]+$/.test(path)) {
      if (method === "GET") return send(res, { pools });
      const poolId = path.endsWith("/pools")
        ? id("pool")
        : path.split("/").at(-1);
      const current = pools.find((p) => p.id === poolId);
      if (current && current.revision !== body.revision)
        return error(res, "Pool changed; refresh");
      const pool = {
        ...body,
        id: poolId,
        org_id: org,
        cloud_id: cloud.id,
        revision: (current?.revision ?? 0) + 1,
      };
      if (current) Object.assign(current, pool);
      else pools.push(pool);
      return send(res, pool);
    }
    if (path.includes("/placements/")) {
      if (path.endsWith("preview-move") || path.endsWith("accept-move"))
        return send(res, {
          input: body,
          review: "move-review",
          placement: {},
          source_server_id: "fixture_host_a",
        });
      return send(res, {
        plan_id: body.plan_id,
        service: body.service,
        review: "recovery-review",
        revision: 1,
        server_id: "fixture_host_a",
        phase: path.endsWith("/recover") ? "released" : "failed",
        active: false,
        started: false,
        storage_retained: true,
      });
    }
    if (path.endsWith("/dns-zones")) {
      if (method === "POST") {
        const zone = {
          ...body,
          id: id("zone"),
          org_id: org,
          revision: 1,
          record_ownership: body.mode === "managed",
          provider_zone: {
            id: "fixture-zone",
            name: body.name,
            account_id: "dns",
            nameservers: ["ns1.example.test"],
          },
          proof_name: `_loomplane.${body.name}`,
          proof_value: "fixture-proof",
          proof_expires_at: "2027-01-01T00:00:00Z",
          verified_at: "0001-01-01T00:00:00Z",
        };
        zones.push(zone);
        return send(res, zone, 201);
      }
      return send(res, {
        zones: path.includes("/projects/")
          ? zones
              .filter((z) => z.project_ids.includes(path.split("/")[4]))
              .map((z) => ({
                id: z.id,
                name: z.name,
                mode: z.mode,
                verified: z.verified_at === stamp,
              }))
          : zones,
      });
    }
    if (path.includes("/dns-zones/")) {
      const zone = zones.find((z) => z.id === path.split("/")[4]);
      if (!zone) return error(res, "Zone missing", 404);
      if (path.endsWith("/verify")) zone.verified_at = stamp;
      else if (path.endsWith("/update")) {
        zone.project_ids = body.project_ids;
        zone.revision++;
      } else if (path.endsWith("/remove")) zones.splice(zones.indexOf(zone), 1);
      return send(res, zone);
    }
    if (path.endsWith("/domain-routes"))
      return send(res, {
        routes: routes.filter((r) => r.project_id === path.split("/")[4]),
      });
    if (path.endsWith("/domain-routes/preview")) {
      const route = {
        id: id("route"),
        org_id: org,
        project_id: path.split("/")[4],
        input: body,
        binding: {
          server_id: "fixture_host_a",
          connection_id: "fixture_ssh",
          address: "192.0.2.20",
          release_id: "fixture_release",
        },
        status: "pending",
        revision: 1,
        updated_at: stamp,
        verification: {
          dns: false,
          tls: false,
          backend: false,
          checked_at: stamp,
          cert_expires: stamp,
        },
      };
      const op = {
        id: id("routeop"),
        kind: "apply",
        status: "preview",
        route,
        desired_record: {
          id: "",
          zone_id: body.zone_id,
          type: "A",
          name: body.hostname,
          value: "192.0.2.20",
          ttl: 300,
        },
        review: id("route-review"),
        attempt: 0,
        revision: 1,
        generation: 3,
        dns_attempted: false,
        message: "DNS and deployment identity pinned",
        expires_at: "2027-01-01T00:00:00Z",
        not_before: "0001-01-01T00:00:00Z",
        created_at: stamp,
      };
      routeOperations.unshift(op);
      return send(res, op);
    }
    if (path.includes("/route-operations/")) {
      const op = routeOperations.find((o) => o.id === path.split("/")[6]);
      if (!op) return error(res, "Route operation missing", 404);
      if (path.endsWith("/apply")) {
        if (body.review !== op.review)
          return error(res, "Immutable review required");
        if (!routes.some((r) => r.id === op.route.id))
          routes.push(structuredClone(op.route));
        progress(op);
      }
      if (path.endsWith("/retry")) {
        routeFailure = false;
        progress(op);
      }
      return send(res, op);
    }
    if (path.includes("/domain-routes/")) {
      const route = routes.find((r) => r.id === path.split("/")[6]);
      if (!route) return error(res, "Route missing", 404);
      if (path.endsWith("/operations"))
        return send(res, {
          operations: routeOperations.filter((o) => o.route.id === route.id),
        });
      if (path.endsWith("/ttl")) {
        if (body.revision !== route.revision)
          return error(res, "Route changed; reload");
        route.input.external_ttl_seconds = body.external_ttl_seconds;
        route.revision++;
        return send(res, route);
      }
      const op = routeOperations.find((o) => o.route.id === route.id);
      if (path.endsWith("/verify")) {
        const verification = {
          ...structuredClone(op),
          id: id("routeop"),
          kind: "verify",
          route: structuredClone(route),
          attempt: 0,
        };
        routeOperations.unshift(verification);
        return send(res, progress(verification));
      }
      if (path.endsWith("/remove")) {
        if (body.revision !== route.revision)
          return error(res, "Route changed; reload");
        route.status = "removed";
        const removal = {
          ...structuredClone(op),
          id: id("routeop"),
          kind: "remove",
          status: "succeeded",
          route: structuredClone(route),
          message: "Only owned route records removed",
        };
        routeOperations.unshift(removal);
        return send(res, removal);
      }
    }
    return proxy(req, res, body);
  } catch (err) {
    send(res, { message: String(err) }, 500);
  }
});
function proxy(req, res, parsed) {
  const headers = { ...req.headers, host: upstream.host };
  const request = http.request(
    new URL(req.url, upstream),
    { method: req.method, headers },
    (response) => {
      res.writeHead(response.statusCode ?? 502, response.headers);
      response.pipe(res);
    },
  );
  request.on("error", () => error(res, "Isolated API unavailable", 502));
  if (parsed && req.method !== "GET") request.end(JSON.stringify(parsed));
  else req.pipe(request);
}
server.listen(port, "127.0.0.1", () =>
  process.stdout.write(
    `Controlled Cloud fixture at http://127.0.0.1:${port}; upstream ${upstream.origin}\n`,
  ),
);
