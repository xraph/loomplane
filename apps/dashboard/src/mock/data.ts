// Everything the mock dashboard shows. Nothing here talks to Loomplane: the
// numbers are made up, but shaped like the real thing (a polyglot monorepo,
// services spread over several clouds, a fifteen-step deployment workflow).

import type { ProjectColorName, ProjectIconName } from "@/lib/project-style"

export type ProviderId = "aws" | "gcp" | "vercel" | "render"
export type Env = "production" | "staging" | "preview"
export type Language = "Go" | "Rust" | "TypeScript" | "JavaScript"
export type Health = "healthy" | "degraded" | "deploying"

export type Person = {
  id: string
  name: string
  email: string
  initials: string
}

export type Provider = {
  id: ProviderId
  name: string
  account: string
  regions: string[]
  status: "connected" | "degraded"
  note?: string
  spend: number
  budget: number
  capabilities: string[]
  connectedBy: string
}

export type Service = {
  id: string
  project: string
  name: string
  kind: "frontend" | "backend" | "worker"
  language: Language
  framework: string
  path: string
  provider: ProviderId
  region: string
  runtime: string
  instances: number | "auto"
  size: string
  p95: number
  rps: number
  errorRate: number
  health: Health
  confidence: "high" | "medium"
  evidence: string[]
  dependsOn: string[]
  domains?: string[]
}

export type Resource = {
  id: string
  project: string
  name: string
  kind: "Postgres" | "Redis" | "Queue" | "Bucket" | "Topic"
  engine: string
  provider: ProviderId
  region: string
  size: string
  cost: number
  status: "available" | "provisioning" | "unconfirmed"
  managedBy: "Ctrl Plane" | "Loomplane"
  // Weakly inferred from code: Loomplane never provisions these unasked.
  inferred?: { evidence: string[] }
}

export type Project = {
  id: string
  name: string
  description: string
  repo: string
  branch: string
  envs: { env: Env; url: string }[]
  manifestRevision: number
  icon: ProjectIconName
  color: ProjectColorName
}

export type StepGroup =
  "Prepare" | "Plan" | "Provision" | "Build" | "Release" | "Verify"

export type Step = {
  key: string
  name: string
  group: StepGroup
  // Typical seconds, before jitter.
  typical: number
  about: string
}

export type PlanChange = {
  action: "create" | "update" | "destroy"
  kind: string
  name: string
  provider: ProviderId
  attrs: { key: string; before?: string; after?: string }[]
  cost: number
}

export type Deployment = {
  id: string
  project: string
  env: Env
  branch: string
  sha: string
  message: string
  author: string
  trigger: "push" | "manual" | "cli" | "api"
  startedAt: number
  // Seconds per step, in workflow order.
  durations: number[]
  requiresApproval: boolean
  approvedAt?: number
  approvedBy?: string
  failedAt?: number
  canceledAt?: number
  changes: PlanChange[]
  dispatchRun: string
  ctrlPlaneRelease: string
}

const loaded = Date.now()
const minute = 60_000
const hour = 60 * minute
const ago = (ms: number) => loaded - ms

// mulberry32: small, seedable, good enough for believable noise.
function rng(seed: number) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export const people: Person[] = [
  { id: "maya", name: "Maya Chen", email: "maya@acme.dev", initials: "MC" },
  { id: "tobi", name: "Tobi Adeyemi", email: "tobi@acme.dev", initials: "TA" },
  { id: "jonas", name: "Jonas Berg", email: "jonas@acme.dev", initials: "JB" },
  { id: "priya", name: "Priya Nair", email: "priya@acme.dev", initials: "PN" },
  { id: "lin", name: "Lin Wei", email: "lin@acme.dev", initials: "LW" },
]

export const me = people[0] as Person

export function person(id: string): Person {
  return people.find((p) => p.id === id) ?? me
}

export const organization = { name: "Acme", slug: "acme", plan: "Team" }

export const providers: Provider[] = [
  {
    id: "aws",
    name: "AWS",
    account: "acme-prod",
    regions: ["us-east-1", "eu-west-1"],
    status: "connected",
    spend: 2184.4,
    budget: 5800,
    capabilities: ["Containers", "Postgres", "Redis", "Queues", "Buckets"],
    connectedBy: "jonas",
  },
  {
    id: "gcp",
    name: "Google Cloud",
    account: "acme-platform",
    regions: ["us-central1"],
    status: "connected",
    spend: 1312.9,
    budget: 3000,
    capabilities: ["Containers", "Postgres", "Buckets", "Pub/Sub"],
    connectedBy: "jonas",
  },
  {
    id: "vercel",
    name: "Vercel",
    account: "acme",
    regions: ["Global edge"],
    status: "connected",
    spend: 640,
    budget: 1500,
    capabilities: ["Frontends", "Edge functions"],
    connectedBy: "tobi",
  },
  {
    id: "render",
    name: "Render",
    account: "acme-tools",
    regions: ["Oregon"],
    status: "degraded",
    note: "Render's API is rate limiting us. Loomplane retries, so syncs run late.",
    spend: 212.75,
    budget: 450,
    capabilities: ["Web services", "Workers", "Postgres"],
    connectedBy: "lin",
  },
]

export function provider(id: ProviderId): Provider {
  return providers.find((p) => p.id === id) as Provider
}

export const projects: Project[] = [
  {
    id: "atlas",
    name: "atlas",
    description: "Storefront, checkout and search",
    repo: "acme/atlas",
    branch: "main",
    envs: [
      { env: "production", url: "atlas.shop" },
      { env: "staging", url: "staging.atlas.shop" },
      { env: "preview", url: "pr-482.atlas.acme.dev" },
    ],
    manifestRevision: 14,
    icon: "store",
    color: "orange",
  },
  {
    id: "beacon",
    name: "beacon",
    description: "Notifications and email",
    repo: "acme/beacon",
    branch: "main",
    envs: [
      { env: "production", url: "beacon.acme.dev" },
      { env: "preview", url: "digest.beacon.acme.dev" },
    ],
    manifestRevision: 6,
    icon: "bell",
    color: "violet",
  },
  {
    id: "ledger",
    name: "ledger",
    description: "Billing and invoicing",
    repo: "acme/ledger",
    branch: "main",
    envs: [
      { env: "production", url: "ledger.acme.dev" },
      { env: "staging", url: "staging.ledger.acme.dev" },
    ],
    manifestRevision: 9,
    icon: "receipt",
    color: "green",
  },
  {
    id: "harbor",
    name: "harbor",
    description: "Internal admin tools",
    repo: "acme/harbor",
    branch: "main",
    envs: [{ env: "production", url: "harbor.acme.internal" }],
    manifestRevision: 3,
    icon: "anchor",
    color: "blue",
  },
]

export function project(id: string): Project | undefined {
  return projects.find((p) => p.id === id)
}

export const services: Service[] = [
  {
    id: "atlas/web",
    project: "atlas",
    name: "web",
    kind: "frontend",
    language: "TypeScript",
    framework: "Next.js 16",
    path: "apps/web",
    provider: "vercel",
    region: "Global edge",
    runtime: "Vercel Functions",
    instances: "auto",
    size: "Fluid compute",
    p95: 84,
    rps: 412,
    errorRate: 0.02,
    health: "healthy",
    confidence: "high",
    evidence: [
      "apps/web/package.json depends on next 16.2",
      "apps/web/next.config.ts",
    ],
    dependsOn: ["atlas/api", "atlas/search"],
    domains: ["atlas.shop"],
  },
  {
    id: "atlas/admin",
    project: "atlas",
    name: "admin",
    kind: "frontend",
    language: "TypeScript",
    framework: "Vite + React",
    path: "apps/admin",
    provider: "vercel",
    region: "Global edge",
    runtime: "Static + edge",
    instances: "auto",
    size: "Static",
    p95: 31,
    rps: 9,
    errorRate: 0,
    health: "healthy",
    confidence: "high",
    evidence: ["apps/admin/vite.config.ts", "apps/admin/index.html"],
    dependsOn: ["atlas/api"],
    domains: ["admin.atlas.shop"],
  },
  {
    id: "atlas/api",
    project: "atlas",
    name: "api",
    kind: "backend",
    language: "Go",
    framework: "chi",
    path: "cmd/api",
    provider: "aws",
    region: "us-east-1",
    runtime: "ECS Fargate",
    instances: 4,
    size: "1 vCPU, 2 GB",
    p95: 46,
    rps: 1290,
    errorRate: 0.08,
    health: "deploying",
    confidence: "high",
    evidence: [
      "go.mod requires github.com/go-chi/chi/v5",
      "cmd/api/main.go listens on :8080",
    ],
    dependsOn: ["atlas/db", "atlas/cache", "atlas/events", "atlas/media"],
    domains: ["api.atlas.shop"],
  },
  {
    id: "atlas/worker",
    project: "atlas",
    name: "worker",
    kind: "worker",
    language: "Go",
    framework: "Go worker",
    path: "cmd/worker",
    provider: "aws",
    region: "us-east-1",
    runtime: "ECS Fargate",
    instances: 2,
    size: "0.5 vCPU, 1 GB",
    p95: 0,
    rps: 0,
    errorRate: 0,
    health: "deploying",
    confidence: "medium",
    evidence: [
      "cmd/worker/main.go has no listener, consumes from SQS",
      "Dockerfile.worker",
    ],
    dependsOn: ["atlas/events", "atlas/db"],
  },
  {
    id: "atlas/search",
    project: "atlas",
    name: "search",
    kind: "backend",
    language: "Rust",
    framework: "axum",
    path: "crates/search",
    provider: "gcp",
    region: "us-central1",
    runtime: "Cloud Run",
    instances: 3,
    size: "2 vCPU, 4 GB",
    p95: 62,
    rps: 380,
    errorRate: 0.01,
    health: "healthy",
    confidence: "high",
    evidence: [
      "crates/search/Cargo.toml depends on axum 0.8",
      "crates/search/src/main.rs binds 0.0.0.0:3000",
    ],
    dependsOn: ["atlas/index"],
  },
  {
    id: "beacon/dispatcher",
    project: "beacon",
    name: "dispatcher",
    kind: "worker",
    language: "Rust",
    framework: "tokio",
    path: "crates/dispatcher",
    provider: "gcp",
    region: "us-central1",
    runtime: "Cloud Run jobs",
    instances: 2,
    size: "1 vCPU, 1 GB",
    p95: 0,
    rps: 0,
    errorRate: 0,
    health: "healthy",
    confidence: "high",
    evidence: ["crates/dispatcher/Cargo.toml depends on tokio"],
    dependsOn: ["beacon/topic", "beacon/db"],
  },
  {
    id: "beacon/templates",
    project: "beacon",
    name: "templates",
    kind: "backend",
    language: "TypeScript",
    framework: "Hono",
    path: "apps/templates",
    provider: "render",
    region: "Oregon",
    runtime: "Render web service",
    instances: 2,
    size: "Standard",
    p95: 120,
    rps: 40,
    errorRate: 0.3,
    health: "degraded",
    confidence: "high",
    evidence: ["apps/templates/package.json depends on hono"],
    dependsOn: ["beacon/db"],
    domains: ["beacon.acme.dev"],
  },
  {
    id: "ledger/api",
    project: "ledger",
    name: "api",
    kind: "backend",
    language: "Go",
    framework: "net/http",
    path: "cmd/ledger",
    provider: "gcp",
    region: "us-central1",
    runtime: "Cloud Run",
    instances: 3,
    size: "1 vCPU, 2 GB",
    p95: 58,
    rps: 210,
    errorRate: 0.04,
    health: "healthy",
    confidence: "high",
    evidence: ["cmd/ledger/main.go listens on :8080"],
    dependsOn: ["ledger/db", "ledger/exports"],
    domains: ["ledger.acme.dev"],
  },
  {
    id: "ledger/reconciler",
    project: "ledger",
    name: "reconciler",
    kind: "worker",
    language: "Go",
    framework: "Go worker",
    path: "cmd/reconciler",
    provider: "gcp",
    region: "us-central1",
    runtime: "Cloud Run jobs",
    instances: 1,
    size: "1 vCPU, 1 GB",
    p95: 0,
    rps: 0,
    errorRate: 0,
    health: "healthy",
    confidence: "medium",
    evidence: ["cmd/reconciler/main.go runs on a schedule"],
    dependsOn: ["ledger/db"],
  },
  {
    id: "harbor/web",
    project: "harbor",
    name: "web",
    kind: "frontend",
    language: "JavaScript",
    framework: "Vite + React",
    path: "web",
    provider: "render",
    region: "Oregon",
    runtime: "Render static site",
    instances: "auto",
    size: "Static",
    p95: 22,
    rps: 3,
    errorRate: 0,
    health: "healthy",
    confidence: "high",
    evidence: ["web/vite.config.js"],
    dependsOn: ["harbor/api"],
    domains: ["harbor.acme.internal"],
  },
  {
    id: "harbor/api",
    project: "harbor",
    name: "api",
    kind: "backend",
    language: "TypeScript",
    framework: "Express",
    path: "api",
    provider: "render",
    region: "Oregon",
    runtime: "Render web service",
    instances: 1,
    size: "Starter",
    p95: 140,
    rps: 4,
    errorRate: 0.1,
    health: "healthy",
    confidence: "high",
    evidence: ["api/package.json depends on express"],
    dependsOn: ["harbor/db"],
  },
]

export const resources: Resource[] = [
  {
    id: "atlas/db",
    project: "atlas",
    name: "atlas-db",
    kind: "Postgres",
    engine: "RDS Postgres 16",
    provider: "aws",
    region: "us-east-1",
    size: "db.r6g.large",
    cost: 412,
    status: "available",
    managedBy: "Ctrl Plane",
  },
  {
    id: "atlas/cache",
    project: "atlas",
    name: "atlas-cache",
    kind: "Redis",
    engine: "ElastiCache Redis 7",
    provider: "aws",
    region: "us-east-1",
    size: "cache.t4g.medium",
    cost: 96,
    status: "available",
    managedBy: "Ctrl Plane",
  },
  {
    id: "atlas/events",
    project: "atlas",
    name: "atlas-events",
    kind: "Queue",
    engine: "SQS standard",
    provider: "aws",
    region: "us-east-1",
    size: "On demand",
    cost: 4.1,
    status: "provisioning",
    managedBy: "Loomplane",
  },
  {
    id: "atlas/index",
    project: "atlas",
    name: "atlas-search-index",
    kind: "Bucket",
    engine: "Cloud Storage",
    provider: "gcp",
    region: "us-central1",
    size: "Standard",
    cost: 18.4,
    status: "available",
    managedBy: "Loomplane",
  },
  {
    id: "atlas/media",
    project: "atlas",
    name: "atlas-media",
    kind: "Bucket",
    engine: "S3",
    provider: "aws",
    region: "us-east-1",
    size: "Standard",
    cost: 0,
    status: "unconfirmed",
    managedBy: "Loomplane",
    inferred: {
      evidence: [
        "packages/media/upload.ts imports @aws-sdk/client-s3",
        "MEDIA_BUCKET is read in packages/media but loomplane.yaml declares no bucket",
      ],
    },
  },
  {
    id: "beacon/db",
    project: "beacon",
    name: "beacon-db",
    kind: "Postgres",
    engine: "Render Postgres 16",
    provider: "render",
    region: "Oregon",
    size: "Basic 1 GB",
    cost: 19,
    status: "available",
    managedBy: "Ctrl Plane",
  },
  {
    id: "beacon/topic",
    project: "beacon",
    name: "beacon-sends",
    kind: "Topic",
    engine: "Pub/Sub",
    provider: "gcp",
    region: "us-central1",
    size: "On demand",
    cost: 12.3,
    status: "available",
    managedBy: "Loomplane",
  },
  {
    id: "ledger/db",
    project: "ledger",
    name: "ledger-db",
    kind: "Postgres",
    engine: "Cloud SQL Postgres 16",
    provider: "gcp",
    region: "us-central1",
    size: "db-custom-2-7680",
    cost: 268,
    status: "available",
    managedBy: "Ctrl Plane",
  },
  {
    id: "ledger/exports",
    project: "ledger",
    name: "ledger-exports",
    kind: "Bucket",
    engine: "Cloud Storage",
    provider: "gcp",
    region: "us-central1",
    size: "Nearline",
    cost: 6.2,
    status: "available",
    managedBy: "Loomplane",
  },
  {
    id: "harbor/db",
    project: "harbor",
    name: "harbor-db",
    kind: "Postgres",
    engine: "Render Postgres 16",
    provider: "render",
    region: "Oregon",
    size: "Basic 256 MB",
    cost: 7,
    status: "available",
    managedBy: "Ctrl Plane",
  },
]

export const steps: Step[] = [
  {
    key: "revision",
    name: "Validate revision",
    group: "Prepare",
    typical: 4,
    about: "Fetch the commit and check it's reachable from the branch.",
  },
  {
    key: "manifest",
    name: "Load manifest",
    group: "Prepare",
    typical: 2,
    about: "Read and validate loomplane.yaml at this revision.",
  },
  {
    key: "deps",
    name: "Resolve dependencies",
    group: "Prepare",
    typical: 6,
    about: "Order services and resources by what depends on what.",
  },
  {
    key: "connections",
    name: "Check cloud connections",
    group: "Prepare",
    typical: 3,
    about:
      "Make sure every cloud this deploy touches still accepts our credentials.",
  },
  {
    key: "capabilities",
    name: "Inspect provider capabilities",
    group: "Prepare",
    typical: 4,
    about:
      "Confirm each region offers the runtimes and resources the plan needs.",
  },
  {
    key: "plan",
    name: "Generate plan",
    group: "Plan",
    typical: 9,
    about: "Work out what to create, change and remove, and what it costs.",
  },
  {
    key: "approval",
    name: "Approval",
    group: "Plan",
    typical: 0.6,
    about: "Plans that remove resources or touch production wait for an admin.",
  },
  {
    key: "approved",
    name: "Validate approved plan",
    group: "Plan",
    typical: 3,
    about: "Check nothing drifted between approval and now.",
  },
  {
    key: "provision",
    name: "Provision resources",
    group: "Provision",
    typical: 40,
    about:
      "Create and update databases, queues and buckets through Ctrl Plane.",
  },
  {
    key: "build",
    name: "Build artifacts",
    group: "Build",
    typical: 70,
    about:
      "Build every changed service in an isolated builder and push images.",
  },
  {
    key: "deploy",
    name: "Deploy services",
    group: "Release",
    typical: 150,
    about: "Roll each service out on its cloud, one instance at a time.",
  },
  {
    key: "endpoints",
    name: "Configure endpoints",
    group: "Release",
    typical: 8,
    about: "Point domains and internal addresses at the new release.",
  },
  {
    key: "health",
    name: "Verify health",
    group: "Verify",
    typical: 30,
    about:
      "Probe health checks and compare latency and errors with the last release.",
  },
  {
    key: "record",
    name: "Record state",
    group: "Verify",
    typical: 2,
    about: "Save the resulting infrastructure state for the next plan.",
  },
  {
    key: "complete",
    name: "Complete",
    group: "Verify",
    typical: 1,
    about: "Mark the release live.",
  },
]

export const APPROVAL_STEP = steps.findIndex((s) => s.key === "approval")
export const stepGroups: StepGroup[] = [
  "Prepare",
  "Plan",
  "Provision",
  "Build",
  "Release",
  "Verify",
]

const commits: Record<string, string[]> = {
  atlas: [
    "Move cart totals to the api",
    "Cache search facets for ten minutes",
    "Fix coupon rounding on checkout",
    "Add order export to admin",
    "Upgrade to Next.js 16.2",
    "Retry payment webhooks with backoff",
  ],
  beacon: [
    "Send weekly digest emails",
    "Batch push notifications per user",
    "Template preview for SMS",
  ],
  ledger: [
    "Prorate plan changes mid-cycle",
    "Reconcile Stripe payouts nightly",
    "Store invoice PDFs in exports",
  ],
  harbor: ["Add feature flag audit view", "Search customers by email"],
}

function hex(r: () => number, n = 7) {
  let s = ""
  for (let i = 0; i < n; i++) s += Math.floor(r() * 16).toString(16)
  return s
}

function jittered(r: () => number, scale = 1) {
  return steps.map((s) => Math.max(0.4, s.typical * scale * (0.75 + r() * 0.5)))
}

function sum(xs: number[], from: number, to: number) {
  let t = 0
  for (let i = from; i < to; i++) t += xs[i] ?? 0
  return t
}

export const changesFor: Record<string, PlanChange[]> = {
  "atlas-prod": [
    {
      action: "create",
      kind: "SQS queue",
      name: "atlas-events",
      provider: "aws",
      attrs: [
        { key: "region", after: "us-east-1" },
        { key: "visibility_timeout", after: "30s" },
        { key: "dead_letter_after", after: "5 receives" },
      ],
      cost: 4.1,
    },
    {
      action: "update",
      kind: "ECS service",
      name: "api",
      provider: "aws",
      attrs: [
        {
          key: "image",
          before: "atlas-api:8c2e1f0",
          after: "atlas-api:a41c9e2",
        },
        { key: "cpu", before: "512", after: "1024" },
        { key: "env.EVENTS_QUEUE_URL", after: "(from atlas-events)" },
      ],
      cost: 34.3,
    },
    {
      action: "update",
      kind: "Vercel project",
      name: "web",
      provider: "vercel",
      attrs: [{ key: "build", before: "8c2e1f0", after: "a41c9e2" }],
      cost: 0,
    },
  ],
  "beacon-preview": [
    {
      action: "create",
      kind: "Pub/Sub subscription",
      name: "beacon-digest",
      provider: "gcp",
      attrs: [
        { key: "topic", after: "beacon-sends" },
        { key: "ack_deadline", after: "60s" },
      ],
      cost: 1.2,
    },
    {
      action: "update",
      kind: "Render web service",
      name: "templates",
      provider: "render",
      attrs: [
        { key: "build", before: "71ab3d2", after: "c09d1f7" },
        { key: "env.DIGEST_DAY", after: "monday" },
      ],
      cost: 0,
    },
    {
      action: "destroy",
      kind: "Cloud Scheduler job",
      name: "beacon-preview-cron",
      provider: "gcp",
      attrs: [{ key: "schedule", before: "*/15 * * * *" }],
      cost: -0.3,
    },
  ],
}

function defaultChanges(p: string, sha: string): PlanChange[] {
  return services
    .filter((s) => s.project === p)
    .slice(0, 2)
    .map((s) => ({
      action: "update" as const,
      kind: s.runtime,
      name: s.name,
      provider: s.provider,
      attrs: [{ key: "build", after: sha }],
      cost: 0,
    }))
}

function seededDeployments(): Deployment[] {
  const r = rng(482)
  const out: Deployment[] = []

  // Live: atlas to production, twenty seconds into rolling services out.
  {
    const durations = jittered(r)
    durations[APPROVAL_STEP] = 94
    const deployAt = steps.findIndex((s) => s.key === "deploy")
    const startedAt = ago((sum(durations, 0, deployAt) + 20) * 1000)
    out.push({
      id: "dpl_9f3a2c",
      project: "atlas",
      env: "production",
      branch: "main",
      sha: "a41c9e2",
      message: "Move cart totals to the api",
      author: "tobi",
      trigger: "push",
      startedAt,
      durations,
      requiresApproval: true,
      approvedAt: startedAt + sum(durations, 0, APPROVAL_STEP + 1) * 1000,
      approvedBy: "maya",
      changes: changesFor["atlas-prod"] ?? [],
      dispatchRun: "run_01JA7Q3M8Z",
      ctrlPlaneRelease: "rel_7d0c22e1",
    })
  }

  // Live: beacon preview, waiting on approval because its plan removes a job.
  {
    const durations = jittered(r)
    const startedAt = ago((sum(durations, 0, APPROVAL_STEP) + 140) * 1000)
    out.push({
      id: "dpl_4b71e0",
      project: "beacon",
      env: "preview",
      branch: "feat/digest-emails",
      sha: "c09d1f7",
      message: "Send weekly digest emails",
      author: "priya",
      trigger: "push",
      startedAt,
      durations,
      requiresApproval: true,
      changes: changesFor["beacon-preview"] ?? [],
      dispatchRun: "run_01JA7Q5B2K",
      ctrlPlaneRelease: "rel_19e4b07a",
    })
  }

  // Recent history, newest first. The first two are pinned so the overview
  // always has a failure to look at.
  const pinned: Partial<Deployment>[] = [
    {
      project: "ledger",
      env: "staging",
      message: "Prorate plan changes mid-cycle",
      author: "jonas",
      failedAt: steps.findIndex((s) => s.key === "health"),
    },
    {
      project: "atlas",
      env: "preview",
      message: "Cache search facets for ten minutes",
      author: "lin",
    },
  ]
  const projectIds = [
    "atlas",
    "atlas",
    "atlas",
    "beacon",
    "ledger",
    "ledger",
    "harbor",
  ]
  const envs: Env[] = ["production", "staging", "preview", "preview"]
  let t = ago(26 * minute)
  for (let i = 0; i < 46; i++) {
    const pin = pinned[i]
    const p =
      pin?.project ??
      (projectIds[Math.floor(r() * projectIds.length)] as string)
    const proj = project(p) as Project
    const allowed = proj.envs.map((e) => e.env)
    let env = pin?.env ?? (envs[Math.floor(r() * envs.length)] as Env)
    if (!allowed.includes(env)) env = allowed[0] as Env
    const durations = jittered(r)
    const prod = env === "production"
    if (prod) durations[APPROVAL_STEP] = 20 + r() * 240
    const roll = r()
    const failedAt =
      pin?.failedAt ??
      (roll < 0.07 ? [9, 10, 12][Math.floor(r() * 3)] : undefined)
    const total = sum(durations, 0, steps.length) * 1000
    const startedAt = t - total
    const sha = hex(r)
    const pool = commits[p] ?? ["Update"]
    const deployment: Deployment = {
      id: `dpl_${hex(r, 6)}`,
      project: p,
      env,
      branch: env === "preview" ? `feat/${hex(r, 4)}` : "main",
      sha,
      message: pin?.message ?? (pool[Math.floor(r() * pool.length)] as string),
      author:
        pin?.author ?? (people[Math.floor(r() * people.length)] as Person).id,
      trigger: (["push", "push", "push", "manual", "cli", "api"] as const)[
        Math.floor(r() * 6)
      ] as Deployment["trigger"],
      startedAt,
      durations,
      requiresApproval: prod,
      approvedAt: prod
        ? startedAt + sum(durations, 0, APPROVAL_STEP + 1) * 1000
        : undefined,
      approvedBy: prod ? (r() < 0.6 ? "maya" : "jonas") : undefined,
      failedAt,
      canceledAt:
        failedAt === undefined && roll > 0.97
          ? startedAt + total * 0.4
          : undefined,
      changes: defaultChanges(p, sha),
      dispatchRun: `run_01JA${hex(r, 6).toUpperCase()}`,
      ctrlPlaneRelease: `rel_${hex(r, 8)}`,
    }
    out.push(deployment)
    t = startedAt - (1.5 + r() * 5) * hour
  }
  return out
}

export const initialDeployments = seededDeployments()

/** Daily deploy outcomes for the last ninety days, oldest first. */
export const deployHistory = (() => {
  const r = rng(90)
  const days: {
    day: number
    succeeded: number
    failed: number
    canceled: number
  }[] = []
  const today = new Date(loaded)
  today.setHours(0, 0, 0, 0)
  for (let i = 89; i >= 0; i--) {
    const day = today.getTime() - i * 24 * hour
    const weekday = new Date(day).getDay()
    const busy = weekday === 0 || weekday === 6 ? 0.25 : 1
    const trend = 0.75 + (90 - i) / 360
    days.push({
      day,
      succeeded: Math.round((7 + r() * 9) * busy * trend),
      failed: r() < 0.45 * busy ? Math.ceil(r() * 2) : 0,
      canceled: r() < 0.2 * busy ? 1 : 0,
    })
  }
  return days
})()

/** Month-to-date spend per day, cumulative, per cloud. */
export const spendSeries = (() => {
  const today = new Date(loaded)
  const days = today.getDate()
  const totals = providers.map((p) => p.spend)
  const r = rng(7)
  return Array.from({ length: days }, (_, i) => {
    const share = (i + 1) / days
    // Today's point is the real month-to-date total; earlier days wobble.
    const wobble = i === days - 1 ? 1 : 0.96 + r() * 0.08
    return {
      day: i + 1,
      total: Math.round(totals.reduce((a, b) => a + b, 0) * share * wobble),
    }
  })
})()

export type Variable = {
  project: string
  key: string
  value: string
  secret: boolean
  envs: Env[]
  source: "Vault" | "Manifest" | "Binding"
  updated: number
  updatedBy: string
}

export const variables: Variable[] = [
  {
    project: "atlas",
    key: "DATABASE_URL",
    value:
      "postgres://atlas:••••@atlas-db.c9x.us-east-1.rds.amazonaws.com/atlas",
    secret: true,
    envs: ["production", "staging", "preview"],
    source: "Binding",
    updated: ago(3 * 24 * hour),
    updatedBy: "jonas",
  },
  {
    project: "atlas",
    key: "REDIS_URL",
    value: "rediss://atlas-cache.x1f.use1.cache.amazonaws.com:6379",
    secret: true,
    envs: ["production", "staging"],
    source: "Binding",
    updated: ago(3 * 24 * hour),
    updatedBy: "jonas",
  },
  {
    project: "atlas",
    key: "EVENTS_QUEUE_URL",
    value: "https://sqs.us-east-1.amazonaws.com/482100931/atlas-events",
    secret: false,
    envs: ["production"],
    source: "Binding",
    updated: ago(4 * minute),
    updatedBy: "tobi",
  },
  {
    project: "atlas",
    key: "STRIPE_SECRET_KEY",
    value: "sk_live_51Hc8yA2eZvKYlo2C0Jv4mQx",
    secret: true,
    envs: ["production"],
    source: "Vault",
    updated: ago(41 * 24 * hour),
    updatedBy: "maya",
  },
  {
    project: "atlas",
    key: "STRIPE_SECRET_KEY",
    value: "sk_test_51Hc8yA2eZvKYlo2CqP0aW",
    secret: true,
    envs: ["staging", "preview"],
    source: "Vault",
    updated: ago(41 * 24 * hour),
    updatedBy: "maya",
  },
  {
    project: "atlas",
    key: "SEARCH_URL",
    value: "https://search-7xq2.a.run.app",
    secret: false,
    envs: ["production", "staging", "preview"],
    source: "Binding",
    updated: ago(9 * 24 * hour),
    updatedBy: "lin",
  },
  {
    project: "atlas",
    key: "NEXT_PUBLIC_CURRENCY",
    value: "USD",
    secret: false,
    envs: ["production", "staging", "preview"],
    source: "Manifest",
    updated: ago(60 * 24 * hour),
    updatedBy: "tobi",
  },
  {
    project: "atlas",
    key: "LOG_LEVEL",
    value: "info",
    secret: false,
    envs: ["production"],
    source: "Manifest",
    updated: ago(12 * 24 * hour),
    updatedBy: "priya",
  },
  {
    project: "beacon",
    key: "DATABASE_URL",
    value:
      "postgres://beacon:••••@dpg-beacon-db.oregon-postgres.render.com/beacon",
    secret: true,
    envs: ["production", "preview"],
    source: "Binding",
    updated: ago(6 * 24 * hour),
    updatedBy: "priya",
  },
  {
    project: "beacon",
    key: "PUBSUB_TOPIC",
    value: "projects/acme-platform/topics/beacon-sends",
    secret: false,
    envs: ["production", "preview"],
    source: "Binding",
    updated: ago(6 * 24 * hour),
    updatedBy: "priya",
  },
  {
    project: "beacon",
    key: "POSTMARK_TOKEN",
    value: "pm_live_8f3c2a91d7e44b0c",
    secret: true,
    envs: ["production"],
    source: "Vault",
    updated: ago(20 * 24 * hour),
    updatedBy: "lin",
  },
  {
    project: "ledger",
    key: "DATABASE_URL",
    value: "postgres://ledger:••••@10.24.0.3:5432/ledger",
    secret: true,
    envs: ["production", "staging"],
    source: "Binding",
    updated: ago(9 * 24 * hour),
    updatedBy: "jonas",
  },
  {
    project: "ledger",
    key: "STRIPE_SECRET_KEY",
    value: "sk_live_51Hc8yA2eZvKYlo2C0Jv4mQx",
    secret: true,
    envs: ["production"],
    source: "Vault",
    updated: ago(41 * 24 * hour),
    updatedBy: "maya",
  },
  {
    project: "ledger",
    key: "EXPORTS_BUCKET",
    value: "gs://ledger-exports",
    secret: false,
    envs: ["production", "staging"],
    source: "Binding",
    updated: ago(9 * 24 * hour),
    updatedBy: "jonas",
  },
  {
    project: "harbor",
    key: "DATABASE_URL",
    value:
      "postgres://harbor:••••@dpg-harbor-db.oregon-postgres.render.com/harbor",
    secret: true,
    envs: ["production"],
    source: "Binding",
    updated: ago(30 * 24 * hour),
    updatedBy: "lin",
  },
]

const atlasManifest = `# loomplane.yaml, revision 14
# Discovered from acme/atlas and confirmed by Tobi Adeyemi.
version: 1
name: atlas

services:
  web:
    path: apps/web
    framework: nextjs
    target: vercel
    domains: [atlas.shop]
    env:
      API_URL: \${services.api.url}
      SEARCH_URL: \${services.search.url}

  admin:
    path: apps/admin
    framework: vite
    target: vercel
    domains: [admin.atlas.shop]

  api:
    path: cmd/api
    language: go
    target: aws/us-east-1
    runtime: container
    port: 8080
    instances: { min: 2, max: 8 }
    resources: [db, cache, events]
    health: /healthz

  worker:
    path: cmd/worker
    language: go
    target: aws/us-east-1
    runtime: container
    resources: [db, events]

  search:
    path: crates/search
    language: rust
    target: gcp/us-central1
    runtime: container
    port: 3000
    resources: [index]

resources:
  db:     { type: postgres, version: 16, target: aws/us-east-1 }
  cache:  { type: redis, target: aws/us-east-1 }
  events: { type: queue, target: aws/us-east-1 }
  index:  { type: bucket, target: gcp/us-central1 }

policies:
  approval:
    production: always
    destroy: always
`

const runtimeOf: Record<Service["kind"], string> = {
  frontend: "static",
  backend: "container",
  worker: "container",
}

/**
 * manifestFor writes a project's loomplane.yaml from what discovery found.
 * atlas has a hand-edited one; a brand new project has nothing yet.
 */
export function manifestFor(p: Project): string {
  if (p.id === "atlas") return atlasManifest
  const svc = services.filter((s) => s.project === p.id)
  const res = resources.filter((r) => r.project === p.id && !r.inferred)
  const key = (id: string) => id.split("/")[1] ?? id
  const lines = [
    `# loomplane.yaml, revision ${p.manifestRevision}`,
    svc.length
      ? `# Discovered from ${p.repo}.`
      : `# Loomplane fills this in when it first reads ${p.repo}.`,
    "version: 1",
    `name: ${p.name}`,
    "",
  ]
  if (!svc.length) {
    lines.push("services: {}", "resources: {}")
    return lines.join("\n") + "\n"
  }
  lines.push("services:")
  for (const s of svc) {
    lines.push(
      `  ${s.name}:`,
      `    path: ${s.path}`,
      `    language: ${s.language.toLowerCase()}`
    )
    lines.push(
      `    target: ${s.provider}/${s.region.toLowerCase().replaceAll(" ", "-")}`
    )
    lines.push(`    runtime: ${runtimeOf[s.kind]}`)
    const uses = s.dependsOn.filter((d) => res.some((r) => r.id === d)).map(key)
    if (uses.length) lines.push(`    resources: [${uses.join(", ")}]`)
    if (s.domains?.length) lines.push(`    domains: [${s.domains.join(", ")}]`)
    lines.push("")
  }
  if (res.length) {
    lines.push("resources:")
    for (const r of res) {
      lines.push(
        `  ${key(r.id)}: { type: ${r.kind.toLowerCase()}, target: ${r.provider}/${r.region.toLowerCase().replaceAll(" ", "-")} }`
      )
    }
  }
  return lines.join("\n") + "\n"
}
