// The generated cache contract only refreshes a screen when a write or a live
// event names a tag some query carries. A tag nothing carries is silent: the
// write lands, and the page stays stale until a reload. This test fails on
// every such tag, so a route declaring one is caught in review rather than by
// someone wondering why the page didn't update.
//
// Matching is exact, as it is in @forge-go/client-core: `Order` reaches only
// a query carrying `Order`, never `Order:7` or `Order[]`.
import { describe, expect, it } from "vitest"

import { entities } from "@/client/forge/loomplane/entities"
import { ops } from "@/client/forge/loomplane/ops"
import { streams } from "@/client/forge/loomplane/stream-bindings"

type Op = {
  method: string
  rootType?: string
  provides: readonly string[]
  invalidates: readonly string[]
}
type Row = { idField?: string; fields?: Record<string, string> }

const all = Object.entries(ops as unknown as Record<string, Op>)
const table = entities as unknown as Record<string, Row>
const reads = all.filter(([, op]) => op.method === "GET")

/** shape turns a tag template into what it matches: `Order:{id}` → `Order:*`. */
const shape = (tag: string) => tag.replace(/\{[^}]+\}/g, "*")

// What a read carries: its declared tags, and an item tag for every entity its
// response can hold, since a settled query also carries the keys it normalized.
const carried = new Set<string>()
const reach = (type: string, seen: Set<string>) => {
  if (seen.has(type)) return
  seen.add(type)
  const row = table[type]
  if (!row) return
  if (row.idField) carried.add(`${type}:*`)
  for (const next of Object.values(row.fields ?? {})) reach(next, seen)
}
for (const [, op] of reads) {
  for (const tag of op.provides) carried.add(shape(tag))
  if (op.rootType) reach(op.rootType, new Set())
}

// Tags known to reach nothing today, each with why. Shrink this list; never
// grow it without a reason a reviewer would accept.
const known: Record<string, string> = {
  EnvironmentCloud:
    "the environment Cloud read has no identity; it goes when Clouds stop being per environment",
  "Watch[]": "release watches are only read one at a time, by deployment",
  "Collection[]":
    "build output collections are delivered to the log view, not listed",
  "Preview[]": "previews are immutable and never listed",
  "Task[]": "agent tasks are only read one at a time",
  "EntryDetail[]": "model registry entries are only read one at a time",
  "CapacityPolicyView[]": "a capacity policy is read per Cloud",
  "CapacityUsage[]": "capacity usage is read per Cloud",
  "ClusterCapacity[]": "cluster capacity is read per cluster",
  ManagedRegistryView: "the managed registry read has no identity",
  "ProviderResourceSync[]": "resource syncs are never listed",
  "GitLabConnectionView[]":
    "GitLab connections are read inside the GitLab status",
  "GitLabHostView[]": "GitLab hosts are read inside the GitLab status",
  "NativeRetentionPolicy[]":
    "carried by the hand-written read in client/native-retention-policies.ts",
  "TLSRenewalAuthority[]": "renewal authorities are never listed",
  "Host[]": "platform hosts are listed as HostView",
  "RouteSummary[]": "gateway routes are read inside the gateway",
  "GroupVerificationResult[]": "verification results are never listed",
  "MemberPrerequisite[]": "prerequisites are never listed",
  "FleetOperation[]": "fleet operations are read one at a time",
  "ProviderResource[]": "the provider resource list has no envelope yet",
  "PushDevice[]": "push devices are never listed",
  "RetentionPolicy[]":
    "carried by the hand-written read in client/retention-policies.ts",
}

function dead(tags: readonly string[]): string[] {
  return tags.map(shape).filter((tag) => !carried.has(tag) && !known[tag])
}

describe("cache contract", () => {
  it("every write invalidates only tags some read carries", () => {
    const misses = all
      .filter(([, op]) => op.method !== "GET")
      .flatMap(([name, op]) =>
        dead(op.invalidates).map((tag) => `${name}: ${tag}`)
      )
    expect(misses).toEqual([])
  })

  it("every live event invalidates only tags some read carries", () => {
    const misses = streams.flatMap((b) =>
      b.kind === "entity"
        ? dead(b.invalidates ?? []).map((tag) => `${b.message}: ${tag}`)
        : []
    )
    expect([...new Set(misses)]).toEqual([])
  })

  it("every known gap is still a gap", () => {
    // A fixed gap left on the list would hide its own regression.
    expect(Object.keys(known).filter((tag) => carried.has(tag))).toEqual([])
  })
})
