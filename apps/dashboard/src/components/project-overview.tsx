// The parts of a project's Overview that say how it fits together: the
// steps from a new project to its first deploy, the things waiting on
// someone, and what runs in each environment.
import { BoxIcon, CheckIcon, ChevronRightIcon } from "lucide-react"
import { cloneElement, type ReactElement, type ReactNode, useMemo } from "react"

import type {
  Binding,
  Environment,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import {
  Cell,
  CloudTable,
  CloudTableRow,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import { CloudSectionHead } from "@/components/cloud-targets/infrastructure-ui"
import { ProductIcon } from "@/components/product-icon"
import { useServiceRows } from "@/components/project-services"
import { QueryError } from "@/components/query-error"
import { toneClass, type Tone } from "@/components/runs"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { kindLabel, resourceLabel, shortSHA } from "@/lib/architecture"
import {
  catalogKind,
  catalogResources,
  parseResourceConfig,
} from "@/lib/catalog-kinds"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

/** One step from a new project to its first deploy. */
export type SetupStep = {
  id: string
  title: string
  /** What the step is for, while it isn't done. */
  detail: ReactNode
  done: boolean
  /** Something is happening on its own, such as an analysis. */
  busy?: boolean
  /** What moves it along, shown only on the step that's next. */
  action?: ReactNode
}

/**
 * SetupSteps lays out how a project gets from nothing to running, in the
 * order the pieces depend on each other, with the next one's action. The
 * page hides it once the project has deployed.
 */
export function SetupSteps({ steps }: { steps: SetupStep[] }) {
  const next = steps.findIndex((s) => !s.done)
  const done = steps.filter((s) => s.done).length
  return (
    <section
      aria-label="Set up"
      className="grid gap-3 rounded-xl bg-card p-4 ring-1 ring-border md:p-5"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-[15px] font-semibold tracking-[-0.01em]">
          Get it running
        </h2>
        <span className="text-[12.5px] text-muted-foreground tabular-nums">
          {done} of {steps.length} done
        </span>
      </div>
      <ol className="grid gap-1">
        {steps.map((step, i) => {
          const current = i === next
          return (
            <li
              key={step.id}
              aria-current={current ? "step" : undefined}
              className={cn(
                "grid grid-cols-[24px_minmax(0,1fr)] items-start gap-3 rounded-lg px-2 py-2",
                current && "bg-muted/60"
              )}
            >
              <StepMark
                n={i + 1}
                done={step.done}
                current={current}
                busy={step.busy}
              />
              <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
                <div className="grid min-w-0 gap-0.5">
                  <span
                    className={cn(
                      "text-[13.5px] font-medium",
                      step.done &&
                        "text-muted-foreground line-through decoration-muted-foreground/40",
                      !step.done && !current && "text-muted-foreground"
                    )}
                  >
                    {step.title}
                    {step.done && <span className="sr-only"> (done)</span>}
                  </span>
                  {current && (
                    <span className="text-[12.5px] leading-normal text-pretty text-muted-foreground">
                      {step.detail}
                    </span>
                  )}
                </div>
                {current && step.action && (
                  <div className="flex shrink-0 flex-wrap items-center gap-2">
                    {step.action}
                  </div>
                )}
              </div>
            </li>
          )
        })}
      </ol>
    </section>
  )
}

function StepMark({
  n,
  done,
  current,
  busy,
}: {
  n: number
  done: boolean
  current: boolean
  busy?: boolean
}) {
  if (done)
    return (
      <span className="mt-px flex size-6 items-center justify-center rounded-full bg-overdye-tint text-overdye-ink">
        <CheckIcon className="size-3.5" strokeWidth={2.6} aria-hidden="true" />
      </span>
    )
  if (busy && current)
    return (
      <span className="mt-px flex size-6 items-center justify-center rounded-full bg-flight-tint text-flight-ink">
        <Spinner className="size-3.5" />
      </span>
    )
  return (
    <span
      className={cn(
        "mt-px flex size-6 items-center justify-center rounded-full font-mono text-[11.5px] tabular-nums",
        current
          ? "bg-primary text-primary-foreground"
          : "text-muted-foreground ring-1 ring-border"
      )}
    >
      {n}
    </span>
  )
}

/** Something on the project that's waiting on a person. */
export type AttentionItem = {
  id: string
  tone: Tone
  title: ReactNode
  detail?: ReactNode
  action?: ReactNode
}

/**
 * NeedsAttention gathers what waits on someone into one list, so the
 * Overview reads as one thing to look at rather than a stack of notices.
 * It shows nothing when nothing waits.
 */
export function NeedsAttention({ items }: { items: AttentionItem[] }) {
  if (items.length === 0) return null
  return (
    <section aria-label="Needs attention" className="grid gap-2.5">
      <CloudSectionHead title="Needs attention" count={items.length} />
      <ul className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
        {items.map((item) => (
          <li
            key={item.id}
            className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3"
          >
            <span
              aria-hidden="true"
              className={cn(
                "size-2 shrink-0 rounded-full",
                toneClass[item.tone].dot
              )}
            />
            <div className="grid min-w-0 flex-[1_1_320px] gap-0.5">
              <span className="text-[13.5px] font-medium text-pretty">
                {item.title}
              </span>
              {item.detail && (
                <span className="text-[12.5px] leading-normal text-pretty text-muted-foreground">
                  {item.detail}
                </span>
              )}
            </div>
            {item.action && (
              <div className="flex shrink-0 items-center gap-2">
                {item.action}
              </div>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

// The Overview shows the first few; the Services view has every one.
const shownServices = 8

/**
 * WhatRunsWhere is the project at a glance: each service and resource in
 * a row, each environment in a column, and in each cell what runs there.
 * It's the map from the code to the places it's deployed. A cell opens
 * that service or resource in that environment.
 */
export function WhatRunsWhere({
  project,
  environments,
  view,
}: {
  project: Project
  environments: Environment[]
  view: ProjectView | null
}) {
  const { rows, bindings, bindingsFailed, refetchBindings } = useServiceRows(
    project,
    environments,
    view
  )
  // One row per resource, holding what runs in each environment, by name.
  const resources = useMemo(() => {
    const out = new Map<string, Record<string, Binding>>()
    for (const e of environments)
      for (const b of catalogResources(bindings[e.name] ?? [])) {
        const row = out.get(b.name) ?? {}
        row[e.name] = b
        out.set(b.name, row)
      }
    return out
  }, [bindings, environments])

  if (environments.length === 0) return null
  const unread = new Set(
    environments.filter((e) => !bindings[e.name]).map((e) => e.name)
  )
  const loading = unread.size > 0 && !bindingsFailed
  // The services come from the graph: until it's here, rows would pop in
  // under the resources already drawn.
  if (!view)
    return (
      <Skeleton
        role="status"
        aria-label="Loading what runs where"
        className="h-28 rounded-xl"
      />
    )
  if (rows.length === 0 && resources.size === 0)
    return loading ? (
      <Skeleton
        role="status"
        aria-label="Loading what runs where"
        className="h-28 rounded-xl"
      />
    ) : null

  const shown = rows.slice(0, shownServices)
  const columns = `minmax(0,1.3fr) ${environments.map(() => "minmax(120px,1fr)").join(" ")}`
  return (
    <section aria-label="What runs where" className="grid gap-2.5">
      <CloudSectionHead
        title="What runs where"
        description="Each service and resource, and what runs of it in each environment."
      />
      {bindingsFailed && (
        <QueryError
          error={bindingsFailed.error}
          fallback="What runs in some environments couldn't be loaded."
          retry={refetchBindings}
        />
      )}
      <CloudTable
        label="What runs where"
        columns={columns}
        minWidth={300 + environments.length * 130}
        head={[
          "",
          ...environments.map((e) => (
            <span key={e.id} className="truncate normal-case">
              {e.name}
            </span>
          )),
        ]}
        footer={
          rows.length > shown.length ? (
            <Link
              to="/projects/$id"
              params={{ id: project.id }}
              search={{ tab: "services" }}
              className="inline-flex items-center gap-1 text-foreground hover:underline hover:underline-offset-4"
            >
              All {rows.length} services
              <ChevronRightIcon className="size-3.5" aria-hidden="true" />
            </Link>
          ) : undefined
        }
      >
        {shown.map((row) => (
          <CloudTableRow key={row.service.id}>
            <NameCell
              tile={<BoxIcon />}
              mono
              name={
                <Link
                  to="/projects/$id/services/$service"
                  params={{ id: project.id, service: row.service.name }}
                  className="text-foreground underline-offset-2 hover:underline"
                >
                  {row.service.name}
                </Link>
              }
              meta={kindLabel(row.service.kind)}
            />
            {environments.map((e) => (
              <Cell key={e.id}>
                <Where
                  binding={row.deployed[e.name]}
                  unread={unread.has(e.name)}
                  loading={loading}
                  link={
                    <Link
                      to="/projects/$id/services/$service"
                      params={{ id: project.id, service: row.service.name }}
                      search={{ environment: e.name }}
                    />
                  }
                />
              </Cell>
            ))}
          </CloudTableRow>
        ))}
        {[...resources].map(([name, byEnvironment]) => {
          const first = environments.find((e) => byEnvironment[e.name])
          const binding = first && byEnvironment[first.name]
          if (!first || !binding) return null
          const config = parseResourceConfig(binding)
          const kind =
            config?.composition?.kind ?? config?.kind ?? binding.engine
          const module = catalogKind(kind)
          const title =
            module?.title ?? (kind ? resourceLabel(kind) : "Resource")
          return (
            <CloudTableRow key={`resource:${name}`}>
              <NameCell
                tile={
                  <ProductIcon
                    product={module?.logo}
                    fallback={BoxIcon}
                    label={title}
                    size="sm"
                  />
                }
                mono
                name={
                  <Link
                    to="/projects/$id/catalog/$resource"
                    params={{ id: project.id, resource: name }}
                    search={{ environment: first.id }}
                    className="text-foreground underline-offset-2 hover:underline"
                  >
                    {name}
                  </Link>
                }
                meta={title}
              />
              {environments.map((e) => (
                <Cell key={e.id}>
                  <Where
                    binding={byEnvironment[e.name]}
                    unread={unread.has(e.name)}
                    loading={loading}
                    link={
                      byEnvironment[e.name] ? (
                        <Link
                          to="/projects/$id/catalog/$resource"
                          params={{ id: project.id, resource: name }}
                          search={{ environment: e.id }}
                        />
                      ) : undefined
                    }
                  />
                </Cell>
              ))}
            </CloudTableRow>
          )
        })}
      </CloudTable>
    </section>
  )
}

/**
 * Where is one cell: the commit that runs, a change made outside
 * Loomplane, or nothing yet.
 */
function Where({
  binding,
  unread,
  loading,
  link,
}: {
  binding?: Binding
  /** Whether what runs in the environment isn't known. */
  unread: boolean
  /** Whether it isn't known because it's still loading. */
  loading: boolean
  link?: ReactElement<{ className?: string }>
}) {
  if (!binding && unread && loading)
    return <Skeleton aria-hidden="true" className="h-4 w-16 rounded" />
  if (!binding)
    return unread ? (
      <span className="text-muted-foreground">Unknown</span>
    ) : (
      <span className="inline-flex items-center gap-1.5 text-muted-foreground">
        <span
          aria-hidden="true"
          className={cn("size-1.5 shrink-0 rounded-full", toneClass.quiet.dot)}
        />
        Not deployed
      </span>
    )
  const tone: Tone = binding.drift ? "waiting" : "live"
  const label = binding.drift
    ? binding.drift === "missing"
      ? "Deleted outside"
      : "Changed outside"
    : binding.commitSha
      ? shortSHA(binding.commitSha)
      : "Live"
  const body = (
    <>
      <span
        aria-hidden="true"
        className={cn("size-1.5 shrink-0 rounded-full", toneClass[tone].dot)}
      />
      <span
        className={cn(
          "truncate",
          !binding.drift && binding.commitSha && "font-mono text-[12px]",
          binding.drift && toneClass[tone].ink
        )}
      >
        {label}
      </span>
      {!binding.drift && <span className="sr-only">, live</span>}
    </>
  )
  if (!link)
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5">{body}</span>
    )
  return cloneElement(
    link,
    {
      className:
        "inline-flex min-w-0 items-center gap-1.5 text-foreground underline-offset-2 hover:underline",
    },
    body
  )
}
