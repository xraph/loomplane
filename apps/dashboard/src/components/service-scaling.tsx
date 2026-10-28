// How many instances of a service run in an environment, and what each gets.
// Saving edits the manifest; the numbers take effect at the next deploy.
import { useInvalidate } from "@forge-go/client-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useManifestsCurrent,
  useManifestsEdit,
} from "@/client/forge/loomplane/hooks"
import type {
  Binding,
  Environment,
  Project,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { useOrganization } from "@/lib/organization"
import { scaleBehaviour } from "@/lib/scaling"
import { cn } from "@/lib/utils"

type Scale = { min: number; max: number }
type Size = { cpu: number; memory: number; gpu?: unknown; shm?: number }
type ServiceEntry = {
  scale?: Scale
  size?: Size
  environments?: Record<string, { scale?: Scale; size?: Size } | null>
}

export function ServiceScaling({
  project,
  name,
  environments,
  deployed,
}: {
  project: Project
  name: string
  environments: Environment[]
  /** The service's running binding in each environment, by name. */
  deployed: Record<string, Binding | undefined>
}) {
  const { can } = useOrganization()
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const [envName, setEnvName] = useState(environments[0]?.name ?? "")
  const services = current.data?.manifest?.services as
    Record<string, ServiceEntry> | undefined
  const entry = services?.[name]
  const override = entry?.environments?.[envName] ?? undefined
  const scale = override?.scale ?? entry?.scale
  const size = override?.size ?? entry?.size
  const behaviour = scaleBehaviour(deployed[envName]?.provider)
  return (
    <section
      aria-label="Scaling"
      className="grid gap-3 rounded-xl px-4 py-3.5 ring-1 ring-border"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid max-w-[60ch] gap-1">
          <h2 className="text-sm font-semibold">Scaling</h2>
          <p className="text-[12.5px] leading-relaxed text-muted-foreground">
            {behaviour?.text ??
              `${name} hasn't run in ${envName} yet. How it uses these numbers depends on where it runs.`}
          </p>
        </div>
        {environments.length > 1 && (
          <div
            role="radiogroup"
            aria-label="Environment"
            className="flex gap-0.5 rounded-lg bg-muted p-[3px]"
          >
            {environments.map((e) => (
              <button
                key={e.id}
                type="button"
                role="radio"
                aria-checked={e.name === envName}
                onClick={() => setEnvName(e.name)}
                className={cn(
                  "h-7 rounded-md px-2.5 text-[12.5px] font-medium",
                  e.name === envName
                    ? "bg-card text-foreground shadow-xs"
                    : "text-muted-foreground hover:text-foreground"
                )}
              >
                {e.name}
              </button>
            ))}
          </div>
        )}
      </div>
      {current.status === "error" ? (
        <QueryError
          error={current.error}
          fallback="The manifest couldn't be loaded."
          retry={current.refetch}
        />
      ) : !current.data ? (
        <Skeleton className="h-16" />
      ) : !entry ? (
        <p className="text-[13px] text-muted-foreground">
          {name} isn&apos;t in the manifest yet, so there&apos;s nothing to
          scale.
        </p>
      ) : behaviour?.kind === "one" || behaviour?.kind === "none" ? (
        <dl className="flex flex-wrap gap-x-6 gap-y-1 rounded-lg bg-subtle px-3 py-2.5 text-[13px]">
          <dt className="text-muted-foreground">Instances</dt>
          <dd>{behaviour.kind === "one" ? "1" : "Set by Vercel"}</dd>
        </dl>
      ) : (
        <ScaleForm
          key={`${envName}:${JSON.stringify([scale, size])}`}
          project={project}
          name={name}
          envName={envName}
          range={behaviour?.kind !== "fixed"}
          scale={scale}
          size={size}
          canEdit={can("project:update")}
        />
      )}
    </section>
  )
}

function ScaleForm({
  project,
  name,
  envName,
  range,
  scale,
  size,
  canEdit,
}: {
  project: Project
  name: string
  envName: string
  /** Whether the provider uses a maximum as well as a minimum. */
  range: boolean
  scale?: Scale
  size?: Size
  canEdit: boolean
}) {
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const [min, setMin] = useState(String(scale?.min ?? 1))
  const [max, setMax] = useState(String(scale?.max ?? 1))
  const [cpu, setCpu] = useState(size ? String(size.cpu) : "")
  const [memory, setMemory] = useState(size ? String(size.memory) : "")
  // A size that asks for GPUs or shm is edited in the manifest, so this
  // form never drops what it doesn't show.
  const special = !!size && (!!size.gpu || !!size.shm)
  const fewest = Number(min)
  const most = range ? Number(max) : fewest
  const sizeSet = cpu !== "" || memory !== ""
  const valid =
    Number.isInteger(fewest) &&
    Number.isInteger(most) &&
    fewest >= 0 &&
    most >= 1 &&
    fewest <= most &&
    (!sizeSet || (Number(cpu) > 0 && Number(memory) > 0))
  const changed =
    fewest !== (scale?.min ?? 1) ||
    most !== (scale?.max ?? 1) ||
    (!special &&
      (Number(cpu || 0) !== (size?.cpu ?? 0) ||
        Number(memory || 0) !== (size?.memory ?? 0)))
  const submit = async () => {
    try {
      await save.mutateAsync({
        path: { id: project.id },
        body: {
          placement: {
            section: "services",
            name,
            environment: envName,
            scale: { min: fewest, max: most },
            ...(special
              ? {}
              : {
                  size: sizeSet
                    ? { cpu: Number(cpu), memory: Number(memory) }
                    : { cpu: 0, memory: 0 },
                }),
          },
        },
      })
      invalidate(useManifestsCurrent)
      toast.success(
        `Saved. ${name} runs with these numbers in ${envName} from its next deploy.`
      )
    } catch (err) {
      toast.error(messageOf(err, "The manifest wasn't changed."))
    }
  }
  const disabled = !canEdit || save.isPending
  return (
    <form
      className="grid gap-2.5"
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <div className="flex flex-wrap items-end gap-3">
        <Compact label={range ? "Fewest instances" : "Instances"}>
          <Input
            type="number"
            min={range ? 0 : 1}
            className="w-24"
            value={min}
            disabled={disabled}
            onChange={(e) => setMin(e.target.value)}
          />
        </Compact>
        {range && (
          <Compact label="Most instances">
            <Input
              type="number"
              min={1}
              className="w-24"
              value={max}
              disabled={disabled}
              onChange={(e) => setMax(e.target.value)}
            />
          </Compact>
        )}
        {!special && (
          <>
            <Compact label="vCPU each">
              <Input
                type="number"
                min={0}
                step={0.25}
                className="w-24"
                placeholder="Default"
                value={cpu}
                disabled={disabled}
                onChange={(e) => setCpu(e.target.value)}
              />
            </Compact>
            <Compact label="Memory each, MiB">
              <Input
                type="number"
                min={0}
                step={128}
                className="w-28"
                placeholder="Default"
                value={memory}
                disabled={disabled}
                onChange={(e) => setMemory(e.target.value)}
              />
            </Compact>
          </>
        )}
        <Button type="submit" disabled={disabled || !valid || !changed}>
          Save to manifest
        </Button>
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">
        {special
          ? "This service asks for GPUs or shared memory, so change its size in the manifest. "
          : ""}
        Saving edits the project&apos;s manifest. The new numbers take effect at
        the next deployment of {envName}.
      </p>
    </form>
  )
}

/** Compact is a labelled field as wide as its input, for a row of numbers. */
function Compact({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <FormField className="w-auto gap-1 text-xs">
      <FieldLabel className="contents">
        {label}
        {children}
      </FieldLabel>
    </FormField>
  )
}
