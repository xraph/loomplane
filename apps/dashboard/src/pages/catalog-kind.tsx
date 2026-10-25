// A catalog kind that lives inside an app project, such as a gateway in
// front of a project's services: pick the project, environment and
// checked target, say where each dependency comes from, name it, and
// Loomplane adds it to the manifest and plans the change. Nothing runs
// until someone approves that plan on its review page. A kind whose
// module brings its own add dialog takes over once the project is chosen.
import {
  BoxIcon,
  CheckIcon,
  ChevronLeftIcon,
  LockIcon,
  PlayIcon,
} from "lucide-react"
import { useState, type FormEvent, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useCatalogEndpointsList,
  useCatalogKindsList,
  useConnectionsList,
  useEnvironmentsList,
  useManifestsCurrent,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  KindInfo,
  Manifest,
  Project,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  CatalogDependencyPicker,
  type ManifestResource,
} from "@/components/catalog-dependency-picker"
import { CloudGlyph } from "@/components/cloud-glyph"
import { Page } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button, buttonVariants } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import {
  addCatalogService,
  freeName,
  initialDependencyPicks,
  takenNames,
  targetsFor,
  usesOf,
  type AddCatalogServiceInput,
  type DependencyPicks,
} from "@/lib/catalog-add"
import {
  catalogKind,
  kindIcon,
  targetLabels,
  type CatalogKindModule,
} from "@/lib/catalog-kinds"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import { protectionLabel, type Protection } from "@/lib/service-recovery"
import { cn } from "@/lib/utils"

/**
 * CatalogKind adds a resource of an app-project kind to an existing
 * project, then opens the plan that adds it.
 */
export function CatalogKind({ kind }: { kind: string }) {
  const kinds = useQuery(useCatalogKindsList)
  const info = kinds.data?.kinds.find((k) => k.name === kind)
  const module = catalogKind(kind)
  const title = module?.title ?? info?.title ?? kind
  return (
    <>
      <SiteHeader
        crumbs={[{ label: "Services", to: "/services" }, { label: title }]}
      />
      <Page>
        <Link
          to="/services"
          className="-mb-2 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          Service catalog
        </Link>
        {kinds.status === "error" ? (
          <Failed
            error={kinds.error}
            retry={kinds.refetch}
            fallback="The catalog couldn't be loaded."
            denied={{
              title: "You can't add catalog services",
              description:
                "Your role doesn't include reading the catalog. An organization owner can give you access.",
            }}
          />
        ) : !kinds.data ? (
          <ZeroState role="status" aria-busy="true">
            <ZeroStateHeader>
              <ZeroStateMedia variant="icon">
                <BoxIcon />
              </ZeroStateMedia>
              <ZeroStateTitle>Loading {title}</ZeroStateTitle>
              <ZeroStateDescription>Reading the catalog.</ZeroStateDescription>
            </ZeroStateHeader>
          </ZeroState>
        ) : !info ? (
          <Missing
            title={`The catalog has no kind called ${kind}`}
            description="It may have been removed from this Loomplane. The catalog lists every service you can add."
          />
        ) : info.placement !== "app-project" ? (
          <Missing
            title={`${title} isn't added to a project from here`}
            description="It runs as part of another service. The catalog lists the services you can add."
          />
        ) : (
          <>
            <Header info={info} module={module} title={title} />
            <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
              <AddForm info={info} module={module} title={title} />
              <WhatYouGet info={info} module={module} />
            </div>
          </>
        )}
      </Page>
    </>
  )
}

function Header({
  info,
  module,
  title,
}: {
  info: KindInfo
  module?: CatalogKindModule
  title: string
}) {
  const blurb = module?.blurb ?? info.description
  return (
    <header className="flex flex-wrap items-center gap-4">
      <ProductIcon
        product={module?.logo}
        fallback={kindIcon(module)}
        label={title}
        size="xl"
      />
      <div className="grid min-w-0 flex-1 gap-1">
        <h1 className="flex flex-wrap items-baseline gap-2 text-[22px] leading-tight font-semibold tracking-[-0.025em] md:text-2xl">
          {title}
          {info.versions[0] && (
            <span className="font-mono text-sm font-normal text-muted-foreground">
              {info.versions[0]}
            </span>
          )}
        </h1>
        {blurb && (
          <p className="max-w-[72ch] text-[13.5px] leading-relaxed text-pretty text-muted-foreground">
            {blurb}
          </p>
        )}
      </div>
    </header>
  )
}

/** WhatYouGet is what runs for the kind and what recovery keeps of it. */
function WhatYouGet({
  info,
  module,
}: {
  info: KindInfo
  module?: CatalogKindModule
}) {
  const runsOn = info.targets.map((t) => targetLabels[t] ?? t)
  return (
    <aside
      aria-label="What you get"
      className="grid gap-3 rounded-[14px] bg-subtle p-4 ring-1 ring-border"
    >
      <h2 className="text-[13px] font-semibold">What you get</h2>
      {(info.components ?? []).length > 0 && (
        <ul className="grid gap-2.5">
          {(info.components ?? []).map((c) => (
            <li key={c.name} className="flex items-start gap-2.5">
              <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-background ring-1 ring-border">
                {c.role === "job" ? (
                  <PlayIcon className="size-3.5" aria-hidden />
                ) : (
                  <BoxIcon className="size-3.5" aria-hidden />
                )}
              </span>
              <span className="grid min-w-0 gap-0.5">
                <span className="text-[12.5px] font-medium">
                  <span className="font-mono">{c.name}</span>{" "}
                  <span className="font-normal text-muted-foreground">
                    {c.role === "job" ? "Job" : "Process"}
                  </span>
                </span>
                <span className="text-xs text-muted-foreground">
                  {protectionLabel(c.protection as Protection) ??
                    protectionLabel("none")}
                </span>
              </span>
            </li>
          ))}
        </ul>
      )}
      <ul className="grid gap-1.5 border-t pt-3 text-[12.5px]">
        {(module?.features ?? []).map((f) => (
          <li key={f} className="flex items-center gap-2">
            <CheckIcon className="size-3.5 text-overdye" />
            {f}
          </li>
        ))}
        <li className="flex items-center gap-2 text-muted-foreground">
          {(module?.features ?? []).length > 0 && <span className="size-3.5" />}
          Runs on {runsOn.join(" or ")}
        </li>
      </ul>
      <p className="border-t pt-3 text-[12px] leading-relaxed text-muted-foreground">
        It joins a project you already have, beside its services. Once it&apos;s
        deployed, its parts and dependencies are on its service page.
      </p>
    </aside>
  )
}

function AddForm({
  info,
  module,
  title,
}: {
  info: KindInfo
  module?: CatalogKindModule
  title: string
}) {
  const projects = useQuery(useProjectsList)
  const [chosen, setChosen] = useState("")
  // A database's own service project isn't where an app-project kind
  // goes: catalog deploys make those with the database icon.
  const list = (projects.data?.projects ?? []).filter(
    (p) => p.icon !== "database"
  )
  const project = list.find((p) => p.id === chosen) ?? list[0]
  return (
    <section
      aria-label={`Add ${title}`}
      className="grid gap-5 rounded-[14px] bg-card p-5 ring-1 ring-border"
    >
      {projects.status === "error" ? (
        <Failed
          error={projects.error}
          retry={projects.refetch}
          fallback="Projects couldn't be loaded."
          denied={{
            title: "You can't see any projects",
            description:
              "Your role doesn't include reading projects. An organization owner can give you access.",
          }}
        />
      ) : !projects.data ? (
        <Skeleton
          role="status"
          aria-label="Loading projects"
          className="h-40"
        />
      ) : !project ? (
        <ZeroState preview={false} surface={false}>
          <ZeroStateHeader>
            <ZeroStateTitle>No projects yet</ZeroStateTitle>
            <ZeroStateDescription>
              {title} is added to a project you already have. Create one with
              its repositories first.
            </ZeroStateDescription>
          </ZeroStateHeader>
          <ZeroStateContent>
            <Link
              to="/projects"
              className={buttonVariants({ variant: "outline" })}
            >
              Open projects
            </Link>
          </ZeroStateContent>
        </ZeroState>
      ) : (
        <>
          <Step n={1} label="Project">
            <Select
              items={list.map((p) => ({ label: p.name, value: p.id }))}
              value={project.id}
              onValueChange={(v) => setChosen(String(v))}
            >
              <SelectTrigger className="h-10 w-full" aria-label="Project">
                <SelectValue>
                  {(v: string) => {
                    const p = list.find((x) => x.id === v)
                    if (!p) return v
                    const s = projectStyle(p)
                    return (
                      <span className="flex items-center gap-2">
                        <ProjectIcon icon={s.icon} color={s.color} size="xs" />
                        {p.name}
                      </span>
                    )
                  }}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {list.map((p) => {
                  const s = projectStyle(p)
                  return (
                    <SelectItem key={p.id} value={p.id}>
                      <ProjectIcon icon={s.icon} color={s.color} size="xs" />
                      {p.name}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
          </Step>
          {module?.addDialog ? (
            <ModuleDialog
              key={project.id}
              dialog={module.addDialog}
              title={title}
              project={project}
            />
          ) : (
            // Keyed so a project's environments, manifest and choices start fresh.
            <ProjectForm
              key={project.id}
              info={info}
              title={title}
              project={project}
            />
          )}
        </>
      )}
    </section>
  )
}

/**
 * ModuleDialog opens a module's own add dialog for the chosen project, in
 * place of the steps below, and opens the plan it makes.
 */
function ModuleDialog({
  dialog: AddDialog,
  title,
  project,
}: {
  dialog: NonNullable<CatalogKindModule["addDialog"]>
  title: string
  project: Project
}) {
  const { can } = useOrganization()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const allowed = can("project:update") && can("deployment:create")
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
      <span className="max-w-[46ch] text-xs leading-relaxed text-muted-foreground">
        {allowed
          ? `Choose how ${title} runs in ${project.name}, then review the plan. Nothing runs until it's approved.`
          : "Adding a service needs project update and deployment create access."}
      </span>
      <Button type="button" disabled={!allowed} onClick={() => setOpen(true)}>
        {`Add ${title} to ${project.name}`}
      </Button>
      {open && (
        <AddDialog
          projectId={project.id}
          projectName={project.name}
          onClose={() => setOpen(false)}
          onPlanned={(planId) => {
            setOpen(false)
            void navigate({ to: "/deployments/$id", params: { id: planId } })
          }}
        />
      )}
    </div>
  )
}

function ProjectForm({
  info,
  title,
  project,
}: {
  info: KindInfo
  title: string
  project: Project
}) {
  const environments = useQuery(useEnvironmentsList, {
    path: { id: project.id },
  })
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  if (environments.status === "error")
    return (
      <Failed
        error={environments.error}
        retry={environments.refetch}
        fallback="The project's environments couldn't be loaded."
      />
    )
  if (current.status === "error" && statusOf(current.error) === 404)
    return (
      <ZeroState preview={false} surface={false}>
        <ZeroStateHeader>
          <ZeroStateTitle>{project.name} isn&apos;t set up yet</ZeroStateTitle>
          <ZeroStateDescription>
            It has no deployment configuration to add {title} to. Open the
            project to save one first.
          </ZeroStateDescription>
        </ZeroStateHeader>
        <ZeroStateContent>
          <Link
            to="/projects/$id"
            params={{ id: project.id }}
            className={buttonVariants({ variant: "outline" })}
          >
            Open project
          </Link>
        </ZeroStateContent>
      </ZeroState>
    )
  if (current.status === "error")
    return (
      <Failed
        error={current.error}
        retry={current.refetch}
        fallback="The project's deployment configuration couldn't be loaded."
      />
    )
  if (!environments.data || !current.data?.manifest)
    return (
      <Skeleton
        role="status"
        aria-label="Loading the project"
        className="h-48"
      />
    )
  const list = environments.data.environments
  if (list.length === 0)
    return (
      <p className="text-[13px] text-muted-foreground">
        {project.name} is still being set up. Its environments appear in a
        moment.
      </p>
    )
  return (
    <Choices
      info={info}
      title={title}
      project={project}
      environments={list}
      manifest={current.data.manifest}
    />
  )
}

const nameRE = /^[a-z][a-z0-9-]{0,39}$/

function Choices({
  info,
  title,
  project,
  environments,
  manifest,
}: {
  info: KindInfo
  title: string
  project: Project
  environments: Environment[]
  manifest: Manifest
}) {
  const { can } = useOrganization()
  const navigate = useNavigate()
  const connections = useQuery(useConnectionsList)
  const taken = takenNames(manifest)
  const [environment, setEnvironment] = useState(
    (environments.find((e) => e.name === "production") ?? environments[0]).name
  )
  const [target, setTarget] = useState("")
  const [picks, setPicks] = useState<DependencyPicks>(() =>
    initialDependencyPicks(info)
  )
  const [name, setName] = useState(() => freeName(info.name, taken))
  const [version, setVersion] = useState(info.versions[0] ?? "")
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState("")
  const [error, setError] = useState("")
  const allowed = can("project:update") && can("deployment:create")
  const targets = targetsFor(info, connections.data?.connections ?? [])
  const selected = targets.find((t) => t.connection.id === target) ?? targets[0]
  const resources: ManifestResource[] = Object.entries(
    manifest.resources ?? {}
  ).flatMap(([resource, r]) =>
    r && typeof r.kind === "string" ? [{ name: resource, kind: r.kind }] : []
  )
  const trimmed = name.trim()
  const nameProblem = !trimmed
    ? ""
    : taken.has(trimmed)
      ? `The manifest already has something called ${trimmed}.`
      : !nameRE.test(trimmed) || trimmed.includes("--")
        ? "Use lower case letters, digits and single hyphens, starting with a letter, at most 40 long."
        : ""
  const locked = pending || !allowed
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (locked || !selected || !trimmed || nameProblem) return
    setPending(true)
    setError("")
    const input: AddCatalogServiceInput = {
      projectId: project.id,
      environment,
      name: trimmed,
      kind: info.name,
      provider: selected.provider,
      target: selected.connection.id,
      version: version || undefined,
      uses: usesOf(info, picks),
    }
    try {
      const { planId } = await addCatalogService(input, setProgress)
      toast.success(`Planning ${trimmed} in ${environment}`, {
        description: "Review the plan, then approve it to deploy.",
      })
      void navigate({ to: "/deployments/$id", params: { id: planId } })
    } catch (cause) {
      setError(messageOf(cause, `${title} wasn't added.`))
      setProgress("")
    } finally {
      setPending(false)
    }
  }
  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-5">
      <Step n={2} label="Environment">
        <Segmented
          label="Environment"
          value={environment}
          options={environments.map((e) => e.name)}
          onChange={setEnvironment}
          disabled={locked}
        />
      </Step>
      <Step n={3} label="Where it runs">
        {connections.status === "error" ? (
          <Failed
            error={connections.error}
            retry={connections.refetch}
            fallback="Targets couldn't be loaded."
          />
        ) : !connections.data ? (
          <Skeleton
            role="status"
            aria-label="Loading checked targets"
            className="h-14"
          />
        ) : targets.length === 0 ? (
          <ZeroState preview={false} surface={false}>
            <ZeroStateHeader>
              <ZeroStateTitle>Connect a server or a cluster</ZeroStateTitle>
              <ZeroStateDescription>
                {title} runs on a checked{" "}
                {info.targets.map((t) => targetLabels[t] ?? t).join(" or ")}{" "}
                target. Add and check one first.
              </ZeroStateDescription>
            </ZeroStateHeader>
            <ZeroStateContent>
              <Link
                to="/integrations"
                className={buttonVariants({ variant: "outline" })}
              >
                Open integrations
              </Link>
            </ZeroStateContent>
          </ZeroState>
        ) : (
          <div
            role="radiogroup"
            aria-label="Deployment target"
            className="grid gap-2 sm:grid-cols-2"
          >
            {targets.map(({ connection: c, provider }) => {
              const on = c.id === selected?.connection.id
              return (
                <button
                  key={c.id}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  disabled={locked}
                  onClick={() => setTarget(c.id)}
                  className={cn(
                    "flex min-w-0 items-center gap-3 rounded-xl bg-card px-3 py-2.5 text-left ring-1 transition-shadow disabled:opacity-60",
                    on
                      ? "ring-2 ring-primary"
                      : "ring-border hover:ring-foreground/25"
                  )}
                >
                  <CloudGlyph cloud={c.cloud} size="md" />
                  <span className="grid min-w-0 gap-0.5">
                    <span className="truncate text-[13px] font-medium">
                      {c.name}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      {provider === "kubernetes"
                        ? `Kubernetes · ${c.details.namespace}`
                        : "Docker on a server"}
                    </span>
                  </span>
                </button>
              )
            })}
          </div>
        )}
      </Step>
      {(info.dependencies ?? []).length > 0 && (
        <Step
          n={4}
          label="Dependencies"
          description="A connected resource has to run on the same target network."
        >
          <Dependencies
            info={info}
            resources={resources}
            value={picks}
            onChange={setPicks}
            disabled={locked}
          />
        </Step>
      )}
      <Step n={(info.dependencies ?? []).length > 0 ? 5 : 4} label="Name">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="grid min-w-0 content-start gap-1.5">
            <Label htmlFor="catalog-resource-name">Resource name</Label>
            <Input
              id="catalog-resource-name"
              value={name}
              maxLength={40}
              required
              disabled={locked}
              aria-invalid={!!nameProblem || undefined}
              aria-describedby="catalog-resource-name-help"
              onChange={(e) => setName(e.target.value)}
            />
            <span
              id="catalog-resource-name-help"
              className={cn(
                "text-xs",
                nameProblem ? "text-destructive" : "text-muted-foreground"
              )}
            >
              {nameProblem || `Its name in ${project.name}'s manifest.`}
            </span>
          </div>
          {info.versions.length > 1 && (
            <div className="grid min-w-0 content-start gap-1.5">
              <span className="text-sm leading-none font-medium">Version</span>
              <Segmented
                label="Version"
                value={version}
                options={info.versions}
                onChange={setVersion}
                disabled={locked}
                mono
              />
            </div>
          )}
        </div>
      </Step>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
        <span className="max-w-[46ch] text-xs leading-relaxed text-muted-foreground">
          Adds {trimmed || "it"} to {project.name}&apos;s manifest and plans{" "}
          {environment}. Nothing runs until the plan is approved.
        </span>
        <Button
          type="submit"
          disabled={locked || !selected || !trimmed || !!nameProblem}
        >
          {pending && <Spinner data-icon="inline-start" />}
          {`Add ${title} and plan`}
        </Button>
      </div>
      {!allowed && (
        <p className="text-[13px] text-muted-foreground">
          Adding a service needs project update and deployment create access.
        </p>
      )}
      {progress && !error && (
        <p role="status" className="text-[13px]">
          {progress}
        </p>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </form>
  )
}

/** Dependencies reads the external endpoints the picker can offer. */
function Dependencies({
  info,
  resources,
  value,
  onChange,
  disabled,
}: {
  info: KindInfo
  resources: ManifestResource[]
  value: DependencyPicks
  onChange: (value: DependencyPicks) => void
  disabled: boolean
}) {
  const endpoints = useQuery(useCatalogEndpointsList)
  const status = endpoints.status === "error" ? statusOf(endpoints.error) : 0
  const external = (info.dependencies ?? []).some((d) => d.external)
  return (
    <div className="grid gap-3">
      {external && endpoints.status === "error" && (
        <>
          {status === 401 || status === 403 ? (
            <p className="text-[12.5px] text-muted-foreground">
              Your role can&apos;t see external endpoints, so only new and
              connected resources are offered.
            </p>
          ) : (
            <QueryError
              error={endpoints.error}
              fallback="External endpoints couldn't be loaded."
              retry={endpoints.refetch}
            />
          )}
        </>
      )}
      <CatalogDependencyPicker
        kind={info}
        resources={resources}
        endpoints={(endpoints.data?.endpoints ?? []).filter(
          (e): e is NonNullable<typeof e> => !!e
        )}
        value={value}
        onChange={onChange}
        disabled={disabled}
      />
    </div>
  )
}

function Step({
  n,
  label,
  description,
  children,
}: {
  n: number
  label: string
  description?: string
  children: ReactNode
}) {
  return (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <h2 className="flex items-center gap-2 text-[13px] font-semibold">
          <span className="flex size-5 items-center justify-center rounded-full bg-muted font-mono text-[11px] text-muted-foreground">
            {n}
          </span>
          {label}
        </h2>
        {description && (
          <span className="text-[12px] text-muted-foreground">
            {description}
          </span>
        )}
      </div>
      {children}
    </div>
  )
}

function Segmented({
  label,
  value,
  options,
  onChange,
  disabled,
  mono = false,
}: {
  label: string
  value: string
  options: string[]
  onChange: (value: string) => void
  disabled: boolean
  mono?: boolean
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="flex max-w-full gap-0.5 self-start justify-self-start overflow-x-auto rounded-[10px] bg-muted p-[3px]"
    >
      {options.map((option) => {
        const on = option === value
        return (
          <button
            key={option}
            type="button"
            role="radio"
            aria-checked={on}
            disabled={disabled}
            onClick={() => onChange(option)}
            className={cn(
              "flex h-7 shrink-0 items-center rounded-lg px-2.5 text-[12.5px] font-medium disabled:opacity-60",
              mono && "font-mono",
              on
                ? "bg-background text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            {option}
          </button>
        )
      })}
    </div>
  )
}

function Missing({
  title,
  description,
}: {
  title: string
  description: string
}) {
  return (
    <ZeroState preview={false} surface={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <BoxIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      <ZeroStateContent>
        <Link to="/services" className={buttonVariants({ variant: "outline" })}>
          Open the catalog
        </Link>
      </ZeroStateContent>
    </ZeroState>
  )
}

// A missing permission isn't a failure to retry, so it reads differently.
function Failed({
  error,
  retry,
  fallback,
  denied,
}: {
  error: unknown
  retry: () => Promise<unknown>
  fallback: string
  denied?: { title: string; description: string }
}) {
  const status = statusOf(error)
  if (status === 401 || status === 403)
    return (
      <ZeroState variant={denied ? "panel" : "inline"} preview={false}>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <LockIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>
            {denied?.title ?? "You can't see this"}
          </ZeroStateTitle>
          <ZeroStateDescription>
            {denied?.description ??
              "Your role doesn't include reading it. An organization owner can give you access."}
          </ZeroStateDescription>
        </ZeroStateHeader>
      </ZeroState>
    )
  return <QueryError error={error} fallback={fallback} retry={retry} />
}
