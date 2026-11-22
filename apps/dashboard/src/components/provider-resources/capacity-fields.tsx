import { useEffect, useState } from "react"
import {
  useConnectionsDigitalOceanDatabaseCapacity as databaseCapacity,
  useConnectionsDatabaseSizes as databaseSizes,
} from "@/client/forge/loomplane/hooks"
import type {
  ResourceSetting,
  DatabaseCapacity,
} from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"

export function useResourceFields(
  provider: string | undefined,
  kind: string | undefined,
  account: string | undefined,
  region: string,
  fields: ResourceSetting[],
  initial: Record<string, string> = {},
  version = "",
  instance = ""
) {
  const fromDO = provider === "digitalocean"
  const live =
    fields.some((f) => f.capacity) &&
    (fromDO ||
      ["rds", "cloud-sql", "azure-database"].includes(provider ?? "") ||
      (!!instance &&
        ["elasticache", "azure-managed-redis"].includes(provider ?? "")))
  const key =
    live && account
      ? [provider, account, kind, region, version, instance].join(":")
      : undefined
  const [attempt, retry] = useState(0)
  const [response, setResponse] = useState<{
    key: string
    attempt: number
    sizes?: string[]
    data?: DatabaseCapacity
    error?: string
  }>()
  useEffect(() => {
    if (!key || !account || !kind) return
    let active = true
    const request = fromDO
      ? databaseCapacity({ path: { id: account } })
          .refetch()
          .then((data) => ({ data }))
      : databaseSizes({
          path: { id: account, kind, region },
          query: { version, instance: instance || undefined },
        })
          .refetch()
          .then(({ sizes }) => ({ sizes }))
    request
      .then((value) => {
        if (active) setResponse({ key, attempt, ...value })
      })
      .catch((cause) => {
        if (active)
          setResponse({
            key,
            attempt,
            error: messageOf(cause, "Database sizes could not be loaded."),
          })
      })
    return () => {
      active = false
    }
  }, [key, attempt, account, kind, region, version, instance, fromDO])
  const result =
    response?.key === key && response?.attempt === attempt
      ? response
      : undefined
  const engine =
    kind === "postgres"
      ? result?.data?.pg
      : kind === "mysql"
        ? result?.data?.mysql
        : result?.data?.valkey
  const layouts = (engine?.layouts ?? []).filter(
    (l) => l.numNodes >= 1 && l.numNodes <= 3 && l.sizes?.length
  )
  const sizes = [...new Set(layouts.flatMap((l) => l.sizes))]
  const [draft, setDraft] = useState<Record<string, string>>({})
  const prefix = [provider, kind, account, region].join(":") + ":"
  const read = (field: ResourceSetting) =>
    draft[prefix + field.name] ?? initial[field.name] ?? field.default ?? ""
  const sizeField = fields.find((f) => f.name === "size")
  const chosenSize = sizeField ? read(sizeField) : ""
  const size = sizes.includes(chosenSize) ? chosenSize : sizes[0]
  const nodes = layouts
    .filter((l) => l.sizes.includes(size))
    .map((l) => String(l.numNodes))
  const choices = (f: ResourceSetting) =>
    live && f.name === "size"
      ? sizes
      : live && f.name === "nodes"
        ? nodes
        : result?.sizes &&
            ["compute_class", "sku", "node_type"].includes(f.name)
          ? f.options?.filter(
              (option) =>
                (instance && option === (initial[f.name] ?? f.default)) ||
                result.sizes?.some(
                  (s) => s.toLowerCase() === option.toLowerCase()
                )
            )
          : f.options
  const values = Object.fromEntries(
    fields.map((f) => {
      const options = choices(f)
      const value = read(f)
      return [
        f.name,
        options && !options.includes(value) ? (options[0] ?? "") : value,
      ]
    })
  )
  const datacenters: Record<string, string> = {
    nyc: "nyc3",
    sfo: "sfo3",
    tor: "tor1",
    atl: "atl1",
    ams: "ams3",
    fra: "fra1",
    lon: "lon1",
    blr: "blr1",
    sgp: "sgp1",
    syd: "syd1",
  }
  const available =
    !region ||
    !engine?.regions?.length ||
    engine.regions.includes(datacenters[region] ?? region)
  const error = live
    ? !key
      ? "Choose a connected account to load database sizes."
      : result?.error ||
        ((result?.data && (!sizes.length || !available)) ||
        (result?.sizes && fields.some((f) => choices(f)?.length === 0))
          ? "The provider offers no supported sizes for this engine and region."
          : "")
    : ""
  const loading = live && !!key && !result
  const valid =
    !loading &&
    !error &&
    fields.every((f) => {
      const v = values[f.name].trim()
      if (!v) return !f.required
      try {
        return !f.pattern || new RegExp("^(?:" + f.pattern + ")$").test(v)
      } catch {
        return false
      }
    })
  return {
    fields,
    values,
    choices,
    edited: fields.some((f) => draft[prefix + f.name] !== undefined),
    valid,
    loading,
    error,
    retry: () => retry((n) => n + 1),
    change: (name: string, value: string) =>
      setDraft((prev) => ({ ...prev, [prefix + name]: value })),
  }
}

export function ResourceFields({
  form,
  disabled,
}: {
  form: ReturnType<typeof useResourceFields>
  disabled?: boolean
}) {
  if (form.loading)
    return (
      <p role="status" className="text-sm">
        Loading database sizes…
      </p>
    )
  if (form.error)
    return (
      <div role="alert" className="flex flex-wrap items-center gap-2 text-sm">
        {form.error}
        <Button type="button" size="sm" variant="outline" onClick={form.retry}>
          Retry
        </Button>
      </div>
    )
  return (
    <div className="grid gap-2 sm:grid-cols-2">
      {form.fields.map((field) => (
        <div key={field.name} className="grid min-w-0 gap-1.5">
          <Label htmlFor={"resource-setting-" + field.name}>
            {field.title}
            {!field.required ? " (optional)" : ""}
          </Label>
          {form.choices(field) ? (
            <select
              id={"resource-setting-" + field.name}
              disabled={disabled}
              value={form.values[field.name]}
              onChange={(e) => form.change(field.name, e.target.value)}
              className="h-8 w-full rounded-md border bg-background px-2 text-sm"
            >
              {form.choices(field)?.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
          ) : (
            <Input
              id={"resource-setting-" + field.name}
              value={form.values[field.name]}
              disabled={disabled}
              required={field.required}
              pattern={field.pattern || undefined}
              onChange={(e) => form.change(field.name, e.target.value)}
            />
          )}
        </div>
      ))}
    </div>
  )
}
