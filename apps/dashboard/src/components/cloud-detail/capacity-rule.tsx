// The fields of one pool's growth rule: the account, region, image and sizes
// new servers come from, how many servers the pool may have, what each new
// server keeps back for the system, and the pool's own tighter limits. The
// pool rule dialog and the new scaling pool dialog both use them.
import { useId } from "react"

import * as api from "@/client/forge/loomplane/hooks"
import type { Connection, ServerOptions } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { Field } from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { Checkbox } from "@/components/ui/checkbox"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { ceilings, read, type RuleDraft } from "@/lib/capacity-policy"
import { gpuTitle } from "@/lib/gpu"

const GIB = 2 ** 30

/**
 * TextAmount is a limit as typed, with its unit in the label. It keeps the
 * text, so an empty field stays empty and is never read as 0.
 */
export function TextAmount({
  label,
  value,
  whole = false,
  onChange,
}: {
  label: string
  value: string
  whole?: boolean
  onChange: (next: string) => void
}) {
  const n = read(value, whole)
  return (
    <FormField className="w-auto gap-1 text-xs">
      <FieldLabel className="contents">
        {label}
        <Input
          type="number"
          min={0}
          step={whole ? 1 : "any"}
          className="w-28"
          value={value}
          aria-invalid={Number.isNaN(n) || undefined}
          onChange={(e) => onChange(e.target.value)}
        />
      </FieldLabel>
    </FormField>
  )
}

/** ReserveAmount is what a new server keeps back, in vCPU or GiB. */
function ReserveAmount({
  label,
  value,
  scale,
  onChange,
}: {
  label: string
  value: number
  scale: number
  onChange: (next: number) => void
}) {
  return (
    <FormField className="w-auto gap-1 text-xs">
      <FieldLabel className="contents">
        {label}
        <Input
          type="number"
          min={0}
          step={0.25}
          className="w-28"
          value={value ? Number((value / scale).toFixed(2)) : ""}
          placeholder="0"
          onChange={(e) =>
            onChange(Math.max(0, Math.round(Number(e.target.value) * scale)))
          }
        />
      </FieldLabel>
    </FormField>
  )
}

/**
 * RuleFields edits one rule. kind is the pool's GPU kind ("" for a CPU
 * pool); sizes and images are offered for that kind only. onTighter turns
 * the pool's own limits on, starting from the Cloud's, or off.
 */
export function RuleFields({
  cloudId,
  kind,
  value,
  accounts,
  set,
  onTighter,
}: {
  cloudId: string
  kind: string
  value: RuleDraft
  accounts: Connection[]
  set: (next: Partial<RuleDraft>) => void
  onTighter: (on: boolean) => void
}) {
  const hint = useId()
  return (
    <div className="grid gap-3">
      <Field label="Account">
        <Select
          value={value.connectionId}
          onValueChange={(v) =>
            set({
              connectionId: v ?? "",
              region: "",
              image: "",
              allowedSizes: [],
            })
          }
        >
          <SelectTrigger aria-label="Account" className="w-full min-w-0">
            <SelectValue placeholder="An account that can create servers" />
          </SelectTrigger>
          <SelectContent>
            {accounts.map((c) => (
              <SelectItem key={c.id} value={c.id}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {value.connectionId && (
        <CatalogFields
          key={value.connectionId}
          cloudId={cloudId}
          kind={kind}
          value={value}
          set={set}
        />
      )}
      <div className="flex flex-wrap gap-3">
        <TextAmount
          label="Fewest servers"
          value={value.minServers}
          whole
          onChange={(minServers) => set({ minServers })}
        />
        <TextAmount
          label="Most servers"
          value={value.maxServers}
          whole
          onChange={(maxServers) => set({ maxServers })}
        />
        <ReserveAmount
          label="Keep back, vCPU"
          value={value.systemReserve.cpuMillis}
          scale={1000}
          onChange={(cpuMillis) =>
            set({ systemReserve: { ...value.systemReserve, cpuMillis } })
          }
        />
        <ReserveAmount
          label="Keep back, GiB"
          value={value.systemReserve.memoryBytes}
          scale={GIB}
          onChange={(memoryBytes) =>
            set({ systemReserve: { ...value.systemReserve, memoryBytes } })
          }
        />
      </div>
      <div className="grid gap-0.5">
        <label className="flex items-start gap-2 text-[13px]">
          <Checkbox
            className="mt-0.5"
            aria-describedby={hint}
            checked={!!value.limits}
            onCheckedChange={(on) => onTighter(on)}
          />
          Tighter limits for this pool
        </label>
        <span id={hint} className="pl-6 text-xs text-muted-foreground">
          Starts from the Cloud&apos;s limits. Lower any of them for this pool
          alone.
        </span>
      </div>
      {value.limits && (
        <div className="flex flex-wrap gap-3">
          {ceilings.map((c) => (
            <TextAmount
              key={c.key}
              label={`Pool ${c.label}`}
              value={value.limits![c.key]}
              whole={c.whole}
              onChange={(v) =>
                set({ limits: { ...value.limits!, [c.key]: v } })
              }
            />
          ))}
          {kind && (
            <TextAmount
              label={`Pool ${gpuTitle(kind)} GPUs`}
              value={value.limits.gpus}
              whole
              onChange={(gpus) => set({ limits: { ...value.limits!, gpus } })}
            />
          )}
        </div>
      )}
    </div>
  )
}

// What a pool may be built from: a GPU pool takes only sizes of its own kind
// and images with NVIDIA drivers, a CPU pool only sizes and images without
// accelerators. Both need an image Loomplane can bootstrap in the region.
function choices(options: ServerOptions, kind: string, value: RuleDraft) {
  const inRegion = (regions: string[]) =>
    !value.region || regions.includes(value.region)
  const sizes = (options.sizes ?? []).filter(
    (s) =>
      inRegion(s.regions) &&
      (!s.architectures?.length ||
        s.architectures.includes(value.architecture)) &&
      (s.accelerator?.kind ?? "") === kind
  )
  const images = (options.images ?? []).filter(
    (i) =>
      i.bootstrap &&
      inRegion(i.regions) &&
      (!i.architecture || i.architecture === value.architecture) &&
      (kind
        ? (i.accelerators ?? []).includes("nvidia")
        : !i.accelerators?.length)
  )
  return { sizes, images }
}

/** CatalogFields offers the regions, images and sizes one account sells. */
function CatalogFields({
  cloudId,
  kind,
  value,
  set,
}: {
  cloudId: string
  kind: string
  value: RuleDraft
  set: (next: Partial<RuleDraft>) => void
}) {
  const options = useQuery(api.useCloudServersOptions, {
    path: { cloudId, connectionId: value.connectionId },
  })
  const catalog = options.data
  const { sizes, images } = catalog
    ? choices(catalog, kind, value)
    : { sizes: [], images: [] }
  return (
    <>
      {options.status === "error" && (
        <QueryError
          error={options.error}
          fallback="This account's regions and sizes couldn't be loaded."
          retry={options.refetch}
        />
      )}
      {catalog && (
        <>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Region">
              <Select
                value={value.region}
                onValueChange={(v) =>
                  set({ region: v ?? "", image: "", allowedSizes: [] })
                }
              >
                <SelectTrigger aria-label="Region" className="w-full min-w-0">
                  <SelectValue placeholder="Choose a region" />
                </SelectTrigger>
                <SelectContent>
                  {catalog.regions.map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.name || r.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Architecture">
              <Select
                value={value.architecture}
                onValueChange={(v) =>
                  set({
                    architecture: v ?? "amd64",
                    image: "",
                    allowedSizes: [],
                  })
                }
              >
                <SelectTrigger
                  aria-label="Architecture"
                  className="w-full min-w-0"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="amd64">x86 (amd64)</SelectItem>
                  <SelectItem value="arm64">Arm (arm64)</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field label="Image">
            <Select
              value={value.image}
              onValueChange={(v) => set({ image: v ?? "" })}
            >
              <SelectTrigger aria-label="Image" className="w-full min-w-0">
                <SelectValue placeholder="Choose an image" />
              </SelectTrigger>
              <SelectContent>
                {images.map((i) => (
                  <SelectItem key={i.id} value={i.id}>
                    {i.name || i.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {value.region && images.length === 0 && (
            <p className="text-[12.5px] text-muted-foreground">
              {kind
                ? "This account offers no image with NVIDIA drivers here."
                : "This account offers no image Loomplane can set up here."}
            </p>
          )}
          <fieldset className="grid min-w-0 gap-1.5">
            <legend className="mb-1.5 text-xs">Sizes it may add</legend>
            {sizes.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground">
                {!value.region
                  ? "Choose a region first."
                  : kind
                    ? `This account offers no ${gpuTitle(kind)} size here.`
                    : "No size here fits this pool's region and architecture."}
              </p>
            ) : (
              sizes.map((s) => {
                const on = value.allowedSizes.includes(s.id)
                return (
                  <FormField
                    key={s.id}
                    orientation="horizontal"
                    className="flex min-w-0 items-center gap-3 rounded-lg px-3 py-2 ring-1 ring-border"
                  >
                    <FieldLabel className="contents">
                      <Checkbox
                        checked={on}
                        onCheckedChange={() =>
                          set({
                            allowedSizes: on
                              ? value.allowedSizes.filter((x) => x !== s.id)
                              : [...value.allowedSizes, s.id],
                          })
                        }
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-[12.5px]">
                        {s.id}
                      </span>
                      <span className="text-right text-xs text-muted-foreground">
                        {s.accelerator
                          ? `${s.accelerator.count}× ${gpuTitle(s.accelerator.kind)} · `
                          : ""}
                        {s.cpus} vCPU ·{" "}
                        {Number((s.memoryMib / 1024).toFixed(1))} GiB
                        {s.hourlyPriceKnown !== false && s.monthlyUsd
                          ? ` · $${Math.round(s.monthlyUsd)} a month`
                          : " · price unknown"}
                      </span>
                    </FieldLabel>
                  </FormField>
                )
              })
            )}
          </fieldset>
        </>
      )}
    </>
  )
}
