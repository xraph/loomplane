import { useState } from "react"
import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useManifestsCurrent,
  useManifestsSave,
} from "@/client/forge/loomplane/hooks"
import type { Finding, Manifest, Plan } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  applyPlacement,
  placementSettings,
  dedicatedBuilders,
} from "@/lib/placement"
import { Link } from "@/lib/router"
import { QueryError } from "./query-error"

export function repairable(f: Finding) {
  return (
    f.subject === "service" &&
    !!f.name &&
    ((f.path?.endsWith(".build.builder") &&
      (f.code.startsWith("builder-") ||
        f.code === "build-isolation-required")) ||
      f.code === "registry-required" ||
      f.code === "host-port-required" ||
      f.code === "need-off")
  )
}

function Pick({
  label,
  options,
  value,
  onChange,
}: {
  label: string
  options: { id: string; name: string }[]
  value: string
  onChange: (id: string) => void
}) {
  return (
    <label className="grid gap-1 text-xs font-medium">
      {label}
      <Select value={value || null} onValueChange={(v) => v && onChange(v)}>
        <SelectTrigger aria-label={label} className="w-full">
          <SelectValue>
            {options.find((o) => o.id === value)?.name ?? "Choose…"}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o.id} value={o.id}>
              {o.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </label>
  )
}

export function PlanRepairs({
  plan,
  onClose,
  replan,
}: {
  plan: Plan
  onClose: () => void
  replan: () => Promise<unknown>
}) {
  const current = useQuery(useManifestsCurrent, {
    path: { id: plan.projectId },
  })
  const connections = useQuery(useConnectionsList)
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const [builder, setBuilder] = useState("")
  const [registry, setRegistry] = useState("")
  const [ports, setPorts] = useState<Record<string, string>>({})
  const [needs, setNeeds] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState("")
  const manifest = current.data?.manifest
  const stale = current.data && current.data.revision !== plan.manifestRevision
  const problems = plan.problems.filter(repairable)
  const affected = (test: (f: Finding) => boolean) => [
    ...new Set(problems.filter(test).map((f) => f.name!)),
  ]
  const builds = affected((f) => !!f.path?.endsWith(".build.builder"))
  const publishes = affected(
    (f) => f.code === "registry-required" || builds.includes(f.name!)
  )
  const publicServices = affected((f) => f.code === "host-port-required")
  const hosts = dedicatedBuilders(connections.data?.connections ?? [])
  const registries = (connections.data?.connections ?? []).filter(
    (c) => c.cloud === "registry" && c.status === "ready"
  )
  const deps = plan.actions.filter(
    (a) =>
      a.op !== "delete" &&
      ((a.subject === "resource" && manifest?.resources?.[a.name]) ||
        (a.subject === "service" &&
          manifest?.services?.[a.name] &&
          a.service?.workload !== "worker"))
  )
  const replacements = affected((f) => f.code === "need-off").flatMap(
    (name) => {
      const declared: string[] = manifest?.services?.[name]?.needs ?? []
      return declared
        .filter((need) => {
          const entry =
            manifest?.resources?.[need] ?? manifest?.services?.[need]
          return (
            entry &&
            (entry.environments?.[plan.environment]?.enabled ??
              entry.enabled) === false
          )
        })
        .map((old) => ({ name, old, key: name + "/" + old }))
    }
  )
  function updated(base: Manifest) {
    let next = structuredClone(base)
    for (const name of new Set([...publishes, ...publicServices])) {
      if (!registry && !ports[name]) continue
      const row = {
        section: "services" as const,
        name,
        kind: "",
        entry: base.services![name],
      }
      const effective = placementSettings(base, row, plan.environment)
      next = applyPlacement(next, row, plan.environment, {
        ...(registry &&
          publishes.includes(name) && {
            build: {
              ...effective.build,
              ...(builds.includes(name) && { builder }),
              registry,
            },
          }),
        ...(ports[name] && {
          endpoint: { ...effective.endpoint, hostPort: Number(ports[name]) },
        }),
      })
    }
    for (const { name, old, key } of replacements) {
      if (!needs[key]) continue
      const entry = next.services![name]
      entry.needs = [
        ...new Set(
          (entry.needs as string[]).map((n) => (n === old ? needs[key] : n))
        ),
      ]
      if (entry.need_options) delete entry.need_options[old]
    }
    return next
  }
  const buildEdit = !!(builder || registry)
  const validBuild =
    !buildEdit ||
    (registries.some((c) => c.id === registry) &&
      (!builds.length || hosts.some((c) => c.id === builder)))
  const ready =
    manifest &&
    !stale &&
    validBuild &&
    Object.values(ports).every(
      (port) => !port || (/^\d+$/.test(port) && +port >= 1 && +port <= 65535)
    ) &&
    (buildEdit ||
      Object.values(ports).some(Boolean) ||
      Object.values(needs).some(Boolean))
  async function submit() {
    if (busy) return
    setBusy(true)
    setError("")
    try {
      if (!saved && !stale) {
        if (!ready || !current.data || !manifest) return
        await save.mutateAsync({
          path: { id: plan.projectId },
          body: {
            baseRevision: current.data.revision,
            manifest: updated(manifest),
          },
        })
        setSaved(true)
        invalidate(useManifestsCurrent)
      }
      await replan()
    } catch (err) {
      setError(messageOf(err, "Couldn't save the plan settings."))
      if (statusOf(err) === 409) void current.refetch()
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(v) => !v && !busy && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Fix deployment plan</DialogTitle>
          <DialogDescription>
            Fill in the settings you want to change, then review a new plan for{" "}
            {plan.environment}.
          </DialogDescription>
        </DialogHeader>
        {saved || stale ? (
          <p>
            {saved
              ? "Changes saved. You can retry planning."
              : "The manifest has changed since this plan. Plan again to review the current settings."}
          </p>
        ) : current.status === "error" ? (
          <QueryError
            error={current.error}
            retry={current.refetch}
            fallback="Couldn't load the manifest."
          />
        ) : !manifest ? (
          <p>Loading manifest…</p>
        ) : (
          <fieldset disabled={busy} className="grid min-w-0 gap-3">
            {publishes.length > 0 && (
              <>
                <p className="text-sm font-medium">
                  Source builds · {publishes.join(", ")}
                </p>
                {connections.status === "error" ? (
                  <QueryError
                    error={connections.error}
                    retry={connections.refetch}
                    fallback="Couldn't load build integrations."
                  />
                ) : (
                  <div className="grid gap-2 sm:grid-cols-2">
                    {!!builds.length && (
                      <Pick
                        label="Dedicated builder"
                        options={hosts}
                        value={builder}
                        onChange={setBuilder}
                      />
                    )}
                    <Pick
                      label="Image registry"
                      options={registries}
                      value={registry}
                      onChange={setRegistry}
                    />
                  </div>
                )}
                <p className="text-xs text-muted-foreground">
                  The worker sends source to the builder. The builder pushes
                  each image to the registry so the target can pull it.{" "}
                  <Link to="/integrations" className="underline">
                    Manage integrations
                  </Link>
                </p>
                {connections.data &&
                  ((builds.length > 0 && !hosts.length) ||
                    !registries.length) && (
                    <p role="status" className="text-xs">
                      Connect{" "}
                      {builds.length > 0 && !hosts.length
                        ? "a dedicated Docker builder"
                        : ""}
                      {builds.length > 0 && !hosts.length && !registries.length
                        ? " and "
                        : ""}
                      {!registries.length ? "a writable image registry" : ""} in
                      Integrations.
                    </p>
                  )}
              </>
            )}
            {publicServices.map((n) => (
              <label key={n} className="grid gap-1 text-xs font-medium">
                Published host port for {n}
                <Input
                  type="number"
                  min={1}
                  max={65535}
                  value={ports[n] ?? ""}
                  onChange={(e) => setPorts({ ...ports, [n]: e.target.value })}
                />
              </label>
            ))}
            {replacements.map(({ name, old, key }) => (
              <Pick
                key={key}
                label={`Replace ${old} for ${name}`}
                value={needs[key] ?? ""}
                options={deps
                  .filter((a) => a.name !== name && a.name !== old)
                  .map((a) => ({ id: a.name, name: a.name }))}
                onChange={(v) => setNeeds({ ...needs, [key]: v })}
              />
            ))}
            {!!replacements.length && (
              <p className="text-xs text-muted-foreground">
                Dependency changes apply to this service in every environment.
              </p>
            )}
          </fieldset>
        )}
        {error && <p role="alert">{error}</p>}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || (!stale && !saved && !ready)}
            onClick={() => void submit()}
          >
            {saved || stale ? "Plan again" : "Save and plan again"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
