import { expect, it } from "vitest"
import type { Backup, Binding, Restore } from "@/client/forge/loomplane/types"
import { protectionOf, recoverySelection } from "@/lib/service-recovery"

it("offers backup and scheduling for live Kubernetes PostgreSQL in the selected environment", () => {
  const binding = {
    name: "database",
    subject: "resource",
    provider: "kubernetes",
    environmentId: "production",
    config: {
      kind: "postgres",
      provider: "kubernetes",
      catalog: { id: "postgresql-17-kubernetes" },
    },
  } as unknown as Binding
  const selection = recoverySelection(
    [binding],
    [] as Backup[],
    [] as Restore[],
    "production",
    ""
  )
  expect(selection.resources).toEqual(["database"])
  expect(selection.live).toBe(true)
  expect(recoverySelection([binding], [], [], "preview", "").live).toBe(false)
  const unsupported = {
    ...binding,
    config: { kind: "mysql", catalog: { id: "mysql-8.4" } },
  } as unknown as Binding
  expect(recoverySelection([unsupported], [], [], "production", "").live).toBe(
    false
  )
})

it("says what each kind of binding keeps", () => {
  const binding = (provider: string, config: unknown) =>
    ({ subject: "resource", provider, config }) as unknown as Binding
  const database = binding("server", {
    kind: "postgres",
    provider: "server",
    catalog: { id: "postgresql-17" },
  })
  expect(protectionOf(database)?.protection).toBe("native")
  const parent = binding("server", {
    kind: "sample",
    provider: "server",
    composition: { kind: "sample", version: "1" },
  })
  expect(protectionOf(parent)).toEqual({
    protection: "none",
    label: "Not backed up, nothing to restore",
  })
  const component = (protection: string) =>
    binding("server", {
      kind: "sample",
      provider: "server",
      component: {
        kind: "sample",
        resource: "app",
        protection,
        component: { name: "api", role: "process" },
      },
    })
  expect(protectionOf(component("none"))?.label).toBe(
    "Not backed up, nothing to restore"
  )
  expect(protectionOf(component("rebuildable"))?.protection).toBe("rebuildable")
  expect(
    protectionOf(undefined, { mode: "external", external: "ep_1" })
  ).toEqual({
    protection: "external",
    label: "Not managed by Loomplane: no backups or restores",
  })
  expect(protectionOf(undefined, { mode: "off" })).toBeUndefined()
  expect(
    protectionOf(undefined, { mode: "new", resource: "app--postgres" })
  ).toBeUndefined()
  const cache = binding("kubernetes", {
    kind: "redis",
    provider: "kubernetes",
    catalog: { id: "redis-8.2-kubernetes" },
  })
  expect(protectionOf(cache)?.protection).toBe("")
})

it("protection copy on Kubernetes", () => {
  const binding = (kind: string) =>
    ({
      subject: "resource",
      provider: "kubernetes",
      config: {
        kind,
        provider: "kubernetes",
        catalog: { id: `${kind}-kubernetes` },
      },
    }) as unknown as Binding
  expect(protectionOf(binding("postgres"))?.protection).toBe("native")
  expect(protectionOf(binding("timescaledb"))).toEqual({
    protection: "",
    label: "Backups on Kubernetes aren’t available yet.",
  })
  expect(protectionOf(binding("redis"))?.label).toBe(
    "Backups on Kubernetes aren’t available yet."
  )
  const server = {
    subject: "resource",
    provider: "server",
    config: { kind: "sample", provider: "server" },
  } as unknown as Binding
  expect(protectionOf(server)?.label).toBe(
    "No Loomplane backups on this target"
  )
})

it("backs up the Postgres a Fabriq provisions as TimescaleDB on a server", () => {
  const binding = (provider: string, kind: string) =>
    ({
      subject: "resource",
      provider,
      config: {
        kind,
        provider,
        catalog: { id: `${kind}-${provider}`, kind },
      },
    }) as unknown as Binding
  expect(protectionOf(binding("server", "timescaledb"))).toEqual({
    protection: "native",
    label: "Native backups and test restores",
  })
  expect(protectionOf(binding("kubernetes", "timescaledb"))?.label).toBe(
    "Backups on Kubernetes aren’t available yet."
  )
  expect(
    recoverySelection(
      [
        {
          ...binding("server", "timescaledb"),
          name: "fabriq--postgres",
          environmentId: "production",
        } as Binding,
      ],
      [],
      [],
      "production",
      ""
    ).live
  ).toBe(true)
})
