// Add a service to a project in one place: something analysis found in
// its code, a database or other kind from the catalog, or any source path.
// Every choice is filled in from what the project already has, the YAML it
// writes is shown before anything is saved, and the result is one manifest
// revision and, if asked, a plan for the environment it came from.
import {
  ArrowLeftIcon,
  CodeIcon,
  FolderGit2Icon,
  SearchIcon,
  SparklesIcon,
} from "lucide-react"
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useMemo,
  useState,
} from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useCatalogEndpointsList,
  useCatalogKindsList,
  useConnectionsList,
  useManifestsCurrent,
  useManifestsSuggestions,
  useProvidersList,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import type {
  Environment,
  KindInfo,
  Manifest,
  Plan,
  Project,
  ServiceSuggestion,
  Target,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useQuery } from "@/client/hooks"
import { CatalogDependencyPicker } from "@/components/catalog-dependency-picker"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ProductIcon } from "@/components/product-icon"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Wiring } from "@/components/service-wiring"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  cloudPick,
  OtherCloudTargets,
  usePlacementContext,
} from "@/components/deployment-targets"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import {
  addService,
  type AddEdit,
  entries,
  envVarFor,
  nameFromSource,
  nameProblem,
  placementsFor,
  preview,
  runnersOf,
  scopeConflicts,
  serviceKinds,
} from "@/lib/add-service"
import {
  freeName,
  initialDependencyPicks,
  takenNames,
  usesOf,
  type DependencyPicks,
} from "@/lib/catalog-add"
import { catalogKind, inCatalog, kindIcon } from "@/lib/catalog-kinds"
import { diffLines, hunks } from "@/lib/diff"
import { kindLabel } from "@/lib/architecture"
import { useProjectCan } from "@/lib/project-permissions"
import { useNavigate } from "@/lib/router"
import { cn } from "@/lib/utils"

export type AddServiceStart = "code" | "catalog" | "source"

type Step =
  | { at: "choose" }
  | { at: "service"; suggestion?: ServiceSuggestion }
  | { at: "catalog"; info: KindInfo }

/**
 * AddServiceDialog adds a service to project. environment is the one it
 * opens on; the person can switch. start picks the first tab, and kind
 * opens straight on a catalog kind's form.
 */
export function AddServiceDialog({
  parentPlan,
  project,
  environments,
  environment: initialEnvironment,
  start = "code",
  open,
  onOpenChange,
}: {
  parentPlan?: Plan
  project: Project
  environments: Environment[]
  environment?: string
  start?: AddServiceStart
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="grid max-h-[90vh] grid-rows-[auto_minmax(0,1fr)] gap-4 overflow-hidden sm:max-w-3xl">
        {open && (
          <Body
            parentPlan={parentPlan}
            project={project}
            environments={environments}
            initialEnvironment={initialEnvironment}
            start={start}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function Body({
  parentPlan,
  project,
  environments,
  initialEnvironment,
  start,
  onClose,
}: {
  parentPlan?: Plan
  project: Project
  environments: Environment[]
  initialEnvironment?: string
  start: AddServiceStart
  onClose: () => void
}) {
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  // A project with no manifest yet starts one with its first add.
  const noManifest =
    current.status === "error" && statusOf(current.error) === 404
  const manifest: Manifest | undefined = noManifest
    ? { version: 1 }
    : current.data?.manifest
  const [step, setStep] = useState<Step>(
    start === "source" ? { at: "service" } : { at: "choose" }
  )
  const [tab, setTab] = useState<AddServiceStart>(start)
  const fallbackEnv =
    parentPlan?.environment ??
    environments.find((e) => e.name === initialEnvironment)?.name ??
    environments.find((e) => e.name === "production")?.name ??
    environments[0]?.name ??
    ""
  const [environment, setEnvironment] = useState(fallbackEnv)

  const title =
    step.at === "choose"
      ? `Add a service to ${project.name}`
      : step.at === "catalog"
        ? `Add ${catalogKind(step.info.name)?.title ?? step.info.title}`
        : step.suggestion
          ? `Add ${step.suggestion.service.name}`
          : "Add a service from your code"

  return (
    <>
      <DialogHeader>
        <div className="flex items-start gap-2">
          {step.at !== "choose" && (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Back to the choices"
              onClick={() => setStep({ at: "choose" })}
            >
              <ArrowLeftIcon />
            </Button>
          )}
          <div className="grid min-w-0 gap-1">
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>
              It goes into {project.name}&apos;s loomplane.yaml as one revision.
              Nothing runs until a plan for it is approved.
            </DialogDescription>
          </div>
        </div>
      </DialogHeader>
      <div className="-mx-1 min-h-0 overflow-y-auto px-1">
        {current.status === "error" && !noManifest ? (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(current.error, "loomplane.yaml couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        ) : !manifest ? (
          <Skeleton
            role="status"
            aria-label="Loading loomplane.yaml"
            className="h-64"
          />
        ) : step.at === "choose" ? (
          <Choose
            project={project}
            tab={tab}
            onTab={setTab}
            onSuggestion={(s) => setStep({ at: "service", suggestion: s })}
            onKind={(info) => setStep({ at: "catalog", info })}
            onSource={() => setStep({ at: "service" })}
          />
        ) : (
          <Configure
            parentPlan={parentPlan}
            key={
              step.at === "catalog"
                ? `kind:${step.info.name}`
                : `svc:${step.suggestion?.id ?? ""}`
            }
            project={project}
            manifest={manifest}
            yaml={noManifest ? "" : (current.data?.yaml ?? "")}
            environments={environments}
            environment={environment}
            onEnvironment={setEnvironment}
            step={step}
            onClose={onClose}
          />
        )}
      </div>
    </>
  )
}

function Choose({
  project,
  tab,
  onTab,
  onSuggestion,
  onKind,
  onSource,
}: {
  project: Project
  tab: AddServiceStart
  onTab: (tab: AddServiceStart) => void
  onSuggestion: (s: ServiceSuggestion) => void
  onKind: (info: KindInfo) => void
  onSource: () => void
}) {
  const suggestions = useQuery(useManifestsSuggestions, {
    path: { id: project.id },
  })
  const found = suggestions.data?.services ?? []
  return (
    <Tabs value={tab} onValueChange={(v) => onTab(v as AddServiceStart)}>
      <TabsList variant="line" aria-label="Where the service comes from">
        <TabsTrigger value="code">
          From your code
          {found.length > 0 && <TabsCount>{found.length}</TabsCount>}
        </TabsTrigger>
        <TabsTrigger value="catalog">Catalog</TabsTrigger>
        <TabsTrigger value="source">Any source path</TabsTrigger>
      </TabsList>
      <TabsContent value="code" className="pt-4">
        {suggestions.status === "error" ? (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(
                suggestions.error,
                "What analysis found couldn't be loaded."
              )}
            </AlertDescription>
          </Alert>
        ) : !suggestions.data ? (
          <Skeleton
            role="status"
            aria-label="Loading what analysis found"
            className="h-40"
          />
        ) : !suggestions.data.analyzed ? (
          <Empty
            title="Nothing analyzed yet"
            action={
              <Button variant="outline" size="sm" onClick={onSource}>
                Add a source path
              </Button>
            }
          >
            Once an analysis of {project.name} finishes, the services it finds
            are listed here, ready to add. You can add one by its path now.
          </Empty>
        ) : found.length === 0 ? (
          <Empty
            title="Every service analysis found is in loomplane.yaml"
            action={
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => onTab("catalog")}
                >
                  Browse the catalog
                </Button>
                <Button variant="outline" size="sm" onClick={onSource}>
                  Add a source path
                </Button>
              </>
            }
          >
            Add a database or cache from the catalog, or a service from a path
            analysis didn&apos;t pick up.
          </Empty>
        ) : (
          <ul aria-label="Services analysis found" className="grid gap-2">
            {found.map((s) => (
              <li key={s.id}>
                <SuggestionButton
                  suggestion={s}
                  onClick={() => onSuggestion(s)}
                />
              </li>
            ))}
          </ul>
        )}
      </TabsContent>
      <TabsContent value="catalog" className="pt-4">
        <Catalog onKind={onKind} />
      </TabsContent>
      <TabsContent value="source" className="pt-4">
        <Empty
          title="A service from any path in your repository"
          action={<Button onClick={onSource}>Choose a path</Button>}
        >
          A directory, a Dockerfile or a Procfile process. Analysis finds what
          it is and how to build it when the plan runs.
        </Empty>
      </TabsContent>
    </Tabs>
  )
}

function SuggestionButton({
  suggestion: s,
  onClick,
}: {
  suggestion: ServiceSuggestion
  onClick: () => void
}) {
  const facts = [
    kindLabel(s.kind),
    s.framework,
    s.service.provider && `on ${s.service.provider}`,
  ].filter(Boolean)
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex w-full min-w-0 items-center gap-3 rounded-xl border bg-card px-3 py-2.5 text-left transition-colors hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
    >
      <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-muted">
        <CodeIcon className="size-4 text-muted-foreground" />
      </span>
      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="flex min-w-0 items-baseline gap-2">
          <span className="truncate text-[13px] font-medium">
            {s.service.name}
          </span>
          <span className="truncate font-mono text-[11.5px] text-muted-foreground">
            {s.service.source}
          </span>
        </span>
        <span className="truncate text-xs text-muted-foreground">
          {facts.join(" · ")}
          {(s.service.needs ?? []).length > 0 &&
            ` · needs ${(s.service.needs ?? []).join(", ")}`}
        </span>
      </span>
      <span className="text-xs font-medium text-brand">Add</span>
    </button>
  )
}

function Catalog({ onKind }: { onKind: (info: KindInfo) => void }) {
  const kinds = useQuery(useCatalogKindsList)
  const [search, setSearch] = useState("")
  if (kinds.status === "error")
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(kinds.error, "The catalog couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  if (!kinds.data)
    return (
      <Skeleton
        role="status"
        aria-label="Loading the catalog"
        className="h-40"
      />
    )
  const q = search.trim().toLowerCase()
  const list = kinds.data.kinds
    .filter(
      (k) =>
        inCatalog(k) &&
        k.targets.some((t) => t === "server" || t === "kubernetes")
    )
    .filter((k) => {
      if (!q) return true
      const m = catalogKind(k.name)
      return [k.name, k.title, m?.title, m?.blurb, k.description]
        .filter(Boolean)
        .some((text) => String(text).toLowerCase().includes(q))
    })
  return (
    <div className="grid gap-3">
      <div className="relative">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          aria-label="Search the catalog"
          placeholder="Postgres, Redis, a gateway…"
          className="pl-8"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>
      {list.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          Nothing in the catalog matches “{search.trim()}”.
        </p>
      ) : (
        <ul aria-label="Catalog" className="grid gap-2 sm:grid-cols-2">
          {list.map((k) => {
            const m = catalogKind(k.name)
            const title = m?.title ?? k.title
            return (
              <li key={k.name}>
                <button
                  type="button"
                  onClick={() => onKind(k)}
                  className="flex h-full w-full min-w-0 items-start gap-3 rounded-xl border bg-card px-3 py-2.5 text-left transition-colors hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:outline-none"
                >
                  <ProductIcon
                    product={m?.logo}
                    fallback={kindIcon(m)}
                    label={title}
                    size="md"
                  />
                  <span className="grid min-w-0 gap-0.5">
                    <span className="truncate text-[13px] font-medium">
                      {title}
                    </span>
                    <span className="line-clamp-2 text-xs text-muted-foreground">
                      {m?.blurb ?? k.description}
                    </span>
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

function Configure({
  parentPlan,
  project,
  manifest,
  yaml,
  environments,
  environment,
  onEnvironment,
  step,
  onClose,
}: {
  parentPlan?: Plan
  project: Project
  manifest: Manifest
  yaml: string
  environments: Environment[]
  environment: string
  onEnvironment: (env: string) => void
  step: Exclude<Step, { at: "choose" }>
  onClose: () => void
}) {
  const can = useProjectCan(project.id)
  const navigate = useNavigate()
  const invalidate = useInvalidate()
  const taken = useMemo(() => takenNames(manifest), [manifest])
  const all = useMemo(() => entries(manifest), [manifest])
  const services = all.filter((e) => e.section === "services")
  const providers = useQuery(useProvidersList)
  const connections = useQuery(useConnectionsList)
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  const endpoints = useQuery(useCatalogEndpointsList)

  const suggestion = step.at === "service" ? step.suggestion : undefined
  const info = step.at === "catalog" ? step.info : undefined
  const module = info ? catalogKind(info.name) : undefined
  const AddDialog = module?.addDialog

  const [name, setName] = useState(() =>
    info ? freeName(info.name, taken) : (suggestion?.service.name ?? "")
  )
  const [nameTouched, setNameTouched] = useState(false)
  const [source, setSource] = useState(suggestion?.service.source ?? "")
  const [repo, setRepo] = useState(suggestion?.service.repo ?? "")
  const [serviceKind, setServiceKind] = useState(suggestion?.service.kind ?? "")
  const [provider, setProvider] = useState(suggestion?.service.provider ?? "")
  const [target, setTarget] = useState(suggestion?.service.target ?? "")
  // A Cloud target, in the Cloud named or the project's default (""),
  // replaces a provider and connection.
  const [onCloud, setOnCloud] = useState<{
    cloud: string
    target: string
  } | null>(null)
  const clouds = usePlacementContext(project.id)
  const [port, setPort] = useState("")
  const [needs, setNeeds] = useState<string[]>(suggestion?.service.needs ?? [])
  const [neededBy, setNeededBy] = useState<string[]>([])
  const [placement, setPlacement] = useState("")
  const [version, setVersion] = useState(info?.versions[0] ?? "")
  const [picks, setPicks] = useState<DependencyPicks>(() =>
    info ? initialDependencyPicks(info) : {}
  )
  const [only, setOnly] = useState(false)
  const [pending, setPending] = useState("")
  const [error, setError] = useState("")

  // A name follows the source until someone types one.
  const onSource = (value: string) => {
    setSource(value)
    if (!nameTouched && !suggestion)
      setName(
        value.trim() ? freeName(nameFromSource(value) || "app", taken) : ""
      )
  }

  const placements = info
    ? placementsFor(
        info,
        connections.data?.connections ?? [],
        providers.data?.providers ?? []
      )
    : []
  const chosenPlacement =
    placements.find((p) => p.key === placement) ?? placements[0]
  const runners = runnersOf(providers.data?.providers ?? [], serviceKind)
  // A Cloud target fits when its provider runs this kind of service, or
  // any kind when analysis will decide.
  const runsHere = (t: Target): boolean => {
    const p = clouds.catalog.find((c) => c.name === t.provider)
    return (
      !p ||
      (p.available &&
        (serviceKind
          ? !!p.runs?.some((r) => r.workload === serviceKind)
          : (p.runs ?? []).length > 0))
    )
  }
  const defaultTargets = clouds.cloudTargets.filter(runsHere)
  const cloudIndex = onCloud?.cloud
    ? clouds.otherClouds.findIndex((c) => c.name === onCloud.cloud)
    : -1
  const runsOnValue = onCloud
    ? onCloud.cloud
      ? `cloud@${cloudIndex}:${onCloud.target}`
      : `cloud:${onCloud.target}`
    : provider || "default"
  const ownTargets = (connections.data?.connections ?? []).filter(
    (c) =>
      c.status === "ready" &&
      c.cloud === provider &&
      (provider !== "kubernetes" || c.details.namespace)
  )
  const repos = sources.data?.sources ?? []

  const trimmed = name.trim()
  const problem = nameProblem(trimmed, taken)
  const conflicts = scopeConflicts(
    manifest,
    environments.map((e) => e.name),
    {
      name: trimmed,
      environment,
      only,
      needs: info ? [] : needs,
      neededBy,
    }
  )
  const portNumber = Number(port)
  const portProblem =
    port.trim() !== "" &&
    (!Number.isInteger(portNumber) || portNumber < 1 || portNumber > 65535)
      ? "A port is a whole number from 1 to 65535."
      : ""

  const edit: AddEdit | undefined = (() => {
    if (!trimmed || problem) return undefined
    const scope = {
      ...(only ? { enabledIn: environment, only: true } : {}),
      ...(neededBy.length ? { neededBy } : {}),
    }
    if (info) {
      if (!chosenPlacement) return undefined
      return {
        addResource: {
          name: trimmed,
          kind: info.name,
          provider: chosenPlacement.provider,
          ...(chosenPlacement.target ? { target: chosenPlacement.target } : {}),
          ...(version ? { version } : {}),
          ...(chosenPlacement.target ? { uses: usesOf(info, picks) } : {}),
          ...scope,
        },
      }
    }
    if (!source.trim() || portProblem) return undefined
    return {
      addService: {
        name: trimmed,
        source: source.trim(),
        ...(repo ? { repo } : {}),
        ...(serviceKind ? { kind: serviceKind } : {}),
        ...(onCloud
          ? {
              cloudTarget: onCloud.target,
              ...(onCloud.cloud ? { cloud: onCloud.cloud } : {}),
            }
          : {
              ...(provider ? { provider } : {}),
              ...(target && (provider === "server" || provider === "kubernetes")
                ? { target }
                : {}),
            }),
        ...(port.trim() ? { port: portNumber } : {}),
        ...(needs.length ? { needs } : {}),
        ...scope,
      },
    }
  })()

  const shown = usePreview(project.id, edit, yaml)
  const mayAdd = can("project:update")
  const mayPlan = mayAdd && can("deployment:create")
  const ready = !!edit && conflicts.length === 0 && !pending

  // A kind that brings its own add dialog takes over from here.
  if (AddDialog)
    return (
      <AddDialog
        parentPlan={parentPlan}
        projectId={project.id}
        projectName={project.name}
        onClose={onClose}
        onPlanned={(planId) => {
          onClose()
          void navigate({ to: "/deployments/$id", params: { id: planId } })
        }}
      />
    )

  const submit = async (plan: boolean) => {
    if (!edit || !ready) return
    setError("")
    try {
      const out = await addService(
        {
          projectId: project.id,
          name: trimmed,
          environment,
          edit,
          plan,
          parentPlan,
        },
        setPending
      )
      invalidate(useManifestsCurrent)
      invalidate(useManifestsSuggestions)
      if (out.planId) {
        toast.success(`Planning ${trimmed} in ${environment}`, {
          description: "Review the plan, then approve it to deploy.",
        })
        onClose()
        void navigate({ to: "/deployments/$id", params: { id: out.planId } })
        return
      }
      toast.success(`Added ${trimmed} to loomplane.yaml`, {
        description: `Revision ${out.revision.revision}. Plan ${environment} to deploy it.`,
      })
      onClose()
    } catch (cause) {
      setError(messageOf(cause, `${trimmed} wasn't added.`))
    } finally {
      setPending("")
    }
  }
  const onSubmit = (event: FormEvent) => {
    event.preventDefault()
    void submit(mayPlan)
  }

  return (
    <form onSubmit={onSubmit} className="grid gap-5">
      {info ? (
        <Field label="Where it runs">
          {connections.status === "error" || providers.status === "error" ? (
            <Alert variant="destructive">
              <AlertDescription>
                {messageOf(
                  connections.error ?? providers.error,
                  "Targets couldn't be loaded."
                )}
              </AlertDescription>
            </Alert>
          ) : !connections.data || !providers.data ? (
            <Skeleton className="h-14" />
          ) : placements.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">
              Nothing connected runs {module?.title ?? info.title} yet. Connect
              a server, a Kubernetes cluster or a cloud account under
              Integrations, then add it here.
            </p>
          ) : (
            <div
              role="radiogroup"
              aria-label="Where it runs"
              className="grid gap-2 sm:grid-cols-2"
            >
              {placements.map((p) => {
                const on = p.key === chosenPlacement?.key
                return (
                  <button
                    key={p.key}
                    type="button"
                    role="radio"
                    aria-checked={on}
                    onClick={() => setPlacement(p.key)}
                    className={cn(
                      "flex min-w-0 items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition-shadow",
                      on
                        ? "border-brand-line bg-brand-soft ring-3 ring-ring/35"
                        : "bg-card hover:bg-muted/50"
                    )}
                  >
                    <CloudGlyph cloud={p.cloud} size="md" />
                    <span className="grid min-w-0 gap-0.5">
                      <span className="truncate text-[13px] font-medium">
                        {p.label}
                      </span>
                      <span className="truncate text-xs text-muted-foreground">
                        {p.detail}
                      </span>
                    </span>
                  </button>
                )
              })}
            </div>
          )}
        </Field>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Source"
            htmlFor="add-service-source"
            help="A directory, a Dockerfile or a Procfile process, such as services/api or apps/web/Dockerfile."
          >
            <Input
              id="add-service-source"
              value={source}
              required
              placeholder="services/api"
              className="font-mono"
              onChange={(e) => onSource(e.target.value)}
            />
          </Field>
          {repos.length > 1 && (
            <Field label="Repository">
              <Select
                value={repo || repos[0]?.alias}
                onValueChange={(v) => setRepo(String(v))}
              >
                <SelectTrigger aria-label="Repository" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {repos.map((r) => (
                    <SelectItem key={r.alias} value={r.alias}>
                      <FolderGit2Icon />
                      {r.alias}
                      <span className="text-muted-foreground">
                        {r.repository}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field label="What it is">
            <Select
              value={serviceKind || "detect"}
              onValueChange={(v) =>
                setServiceKind(v === "detect" ? "" : String(v))
              }
            >
              <SelectTrigger aria-label="What it is" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="detect">
                  {suggestion
                    ? `${kindLabel(suggestion.kind)}, as analysis found`
                    : "Whatever analysis finds"}
                </SelectItem>
                {serviceKinds.map((k) => (
                  <SelectItem key={k.value} value={k.value}>
                    {k.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="Runs on">
            <Select
              value={runsOnValue}
              onValueChange={(v) => {
                const value = String(v)
                setTarget("")
                if (value.startsWith("cloud:") || value.startsWith("cloud@")) {
                  const pick = cloudPick(value, clouds.otherClouds)
                  setOnCloud({
                    cloud: pick.cloud ?? "",
                    target: pick.cloudTarget ?? "",
                  })
                  setProvider("")
                  return
                }
                setOnCloud(null)
                setProvider(value === "default" ? "" : value)
              }}
            >
              <SelectTrigger aria-label="Runs on" className="w-full">
                <SelectValue>
                  {onCloud
                    ? `${onCloud.target}${onCloud.cloud ? ` in ${onCloud.cloud}` : ""}`
                    : undefined}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">
                  {manifest.defaults?.provider
                    ? `The default, ${manifest.defaults.provider}`
                    : "The manifest's default"}
                </SelectItem>
                {defaultTargets.length > 0 && (
                  <SelectGroup>
                    <SelectLabel>
                      {clouds.cloudName ?? "Cloud targets"}
                    </SelectLabel>
                    {defaultTargets.map((t) => (
                      <SelectItem key={t.id} value={`cloud:${t.name}`}>
                        {t.displayName || t.name}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                )}
                {clouds.otherClouds.map((c, i) => (
                  <OtherCloudTargets
                    key={c.id}
                    projectId={project.id}
                    cloud={c}
                    index={i}
                    runsHere={runsHere}
                  />
                ))}
                <SelectGroup>
                  {(defaultTargets.length > 0 ||
                    clouds.otherClouds.length > 0) && (
                    <SelectLabel>Accounts</SelectLabel>
                  )}
                  {runners.map((p) => (
                    <SelectItem key={p.name} value={p.name}>
                      <CloudGlyph cloud={p.cloud} />
                      {p.title}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </Field>
          {!onCloud && (provider === "server" || provider === "kubernetes") && (
            <Field label="Target">
              <Select
                value={target || ownTargets[0]?.id || ""}
                onValueChange={(v) => setTarget(String(v))}
              >
                <SelectTrigger aria-label="Target" className="w-full">
                  <SelectValue placeholder="Connect one under Integrations" />
                </SelectTrigger>
                <SelectContent>
                  {ownTargets.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field
            label="Port"
            htmlFor="add-service-port"
            help={portProblem || "Only when its Dockerfile doesn't say."}
            invalid={!!portProblem}
          >
            <Input
              id="add-service-port"
              inputMode="numeric"
              placeholder={suggestion?.ports?.[0]?.toString() ?? "8080"}
              value={port}
              aria-invalid={!!portProblem || undefined}
              onChange={(e) => setPort(e.target.value)}
            />
          </Field>
        </div>
      )}

      {info &&
        chosenPlacement?.target &&
        (info.dependencies ?? []).length > 0 && (
          <Field label="Its dependencies">
            <CatalogDependencyPicker
              kind={info}
              resources={all
                .filter((e) => e.section === "resources" && e.kind)
                .map((e) => ({ name: e.name, kind: e.kind ?? "" }))}
              endpoints={(endpoints.data?.endpoints ?? []).filter(
                (e): e is NonNullable<typeof e> => e !== null
              )}
              value={picks}
              onChange={setPicks}
            />
          </Field>
        )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Name"
          htmlFor="add-service-name"
          help={problem || "How the rest of loomplane.yaml refers to it."}
          invalid={!!problem}
        >
          <Input
            id="add-service-name"
            value={name}
            maxLength={40}
            required
            className="font-mono"
            aria-invalid={!!problem || undefined}
            onChange={(e) => {
              setNameTouched(true)
              setName(e.target.value)
            }}
          />
        </Field>
        {info && info.versions.length > 1 && (
          <Field label="Version">
            <Select
              value={version}
              onValueChange={(v) => setVersion(String(v))}
            >
              <SelectTrigger aria-label="Version" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {info.versions.map((v) => (
                  <SelectItem key={v} value={v}>
                    {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
      </div>

      {!info && (
        <Wiring
          label="It needs"
          help="Each one it needs gets it an address in a variable, and deploys first."
          options={all.filter((e) => e.name !== trimmed)}
          value={needs}
          onChange={setNeeds}
          hint={(e) => envVarFor(e, e.section)}
        />
      )}
      <Wiring
        label="Used by"
        help={`Each service picked gets ${trimmed || "its"} address${info ? "" : ` in ${trimmed ? envVarFor({ name: trimmed }, "services") : "a variable"}`}.`}
        options={services.filter((e) => e.name !== trimmed)}
        value={neededBy}
        onChange={setNeededBy}
        hint={() =>
          info
            ? envVarFor(
                { name: trimmed || info.name, kind: info.name },
                "resources"
              )
            : envVarFor({ name: trimmed || "service" }, "services")
        }
      />

      <Field label="Environments">
        <div
          role="radiogroup"
          aria-label="Environments it runs in"
          className="flex flex-wrap items-center gap-2"
        >
          {[
            { on: false, label: "Every environment" },
            { on: true, label: `Only ${environment}` },
          ].map((o) => (
            <button
              key={o.label}
              type="button"
              role="radio"
              aria-checked={only === o.on}
              onClick={() => setOnly(o.on)}
              className={cn(
                "rounded-lg border px-3 py-1.5 text-[13px] transition-shadow",
                only === o.on
                  ? "border-brand-line bg-brand-soft ring-3 ring-ring/35"
                  : "bg-card hover:bg-muted/50"
              )}
            >
              {o.label}
            </button>
          ))}
          {!parentPlan && environments.length > 1 && (
            <Select
              value={environment}
              onValueChange={(v) => onEnvironment(String(v))}
            >
              <SelectTrigger
                aria-label="Environment to plan"
                size="sm"
                className="ml-auto w-auto"
              >
                <span className="text-muted-foreground">Plan</span>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {environments.map((e) => (
                  <SelectItem key={e.id} value={e.name}>
                    {e.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </div>
        {conflicts.length > 0 && (
          <Alert variant="destructive" className="mt-2">
            <AlertDescription>
              <ul className="grid gap-1">
                {conflicts.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
              Add it to every environment, or leave those services out.
            </AlertDescription>
          </Alert>
        )}
      </Field>

      <Preview state={shown} />

      {error && (
        <Alert variant="destructive">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <DialogFooter className="items-center sm:justify-between">
        <span role="status" className="text-xs text-muted-foreground">
          {pending ||
            (!mayAdd
              ? "Adding a service needs permission to change the project."
              : !mayPlan
                ? "You can add it; someone who can deploy plans it."
                : `Adds it, then plans ${environment} for you to review.`)}
        </span>
        <div className="flex gap-2">
          {mayPlan && (
            <Button
              type="button"
              variant="outline"
              disabled={!ready || !mayAdd}
              onClick={() => void submit(false)}
            >
              Add only
            </Button>
          )}
          <Button type="submit" disabled={!ready || !mayAdd}>
            {pending && <Spinner data-icon="inline-start" />}
            {mayPlan ? `Add and plan ${environment}` : "Add to loomplane.yaml"}
          </Button>
        </div>
      </DialogFooter>
    </form>
  )
}

type PreviewState =
  | { state: "idle" }
  | { state: "checking" }
  | { state: "ok"; yaml: string; before: string }
  | { state: "refused"; message: string }

/**
 * usePreview asks the server what the edit would save, a moment after the
 * form settles, so the YAML shown and any refusal are the server's own.
 */
function usePreview(
  projectId: string,
  edit: AddEdit | undefined,
  before: string
): PreviewState {
  const key = edit ? JSON.stringify(edit) : ""
  // The last answer, and the request it answered: an answer to an older
  // request means the current one is still being checked.
  const [answer, setAnswer] = useState<{
    key: string
    state: PreviewState
  } | null>(null)
  useEffect(() => {
    if (!key) return
    let live = true
    const timer = setTimeout(() => {
      preview(projectId, JSON.parse(key) as AddEdit)
        .then((rev) => {
          if (live)
            setAnswer({ key, state: { state: "ok", yaml: rev.yaml, before } })
        })
        .catch((cause: unknown) => {
          if (live)
            setAnswer({
              key,
              state: {
                state: "refused",
                message: messageOf(cause, "loomplane.yaml wouldn't take it."),
              },
            })
        })
    }, 350)
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [projectId, key, before])
  if (!key) return { state: "idle" }
  if (answer?.key !== key) return { state: "checking" }
  return answer.state
}

function Preview({ state }: { state: PreviewState }) {
  if (state.state === "idle") return null
  if (state.state === "refused")
    return (
      <Alert variant="destructive">
        <AlertDescription>{state.message}</AlertDescription>
      </Alert>
    )
  return (
    <section aria-label="Changes to loomplane.yaml" className="grid gap-1.5">
      <h3 className="flex items-center gap-2 text-[13px] font-medium">
        <SparklesIcon className="size-3.5 text-muted-foreground" />
        Changes to loomplane.yaml
        {state.state === "checking" && <Spinner className="size-3" />}
      </h3>
      {state.state === "ok" && (
        <pre className="max-h-56 overflow-auto rounded-lg border bg-muted/30 py-2 font-mono text-xs leading-relaxed">
          {hunks(diffLines(state.before, state.yaml), 1).map((h, i) =>
            h.kind === "folded" ? (
              <div key={i} className="px-3 text-muted-foreground select-none">
                ⋯
              </div>
            ) : (
              h.lines.map((l, j) => (
                <div
                  key={`${i}.${j}`}
                  className={cn(
                    "px-3 whitespace-pre",
                    l.kind === "added" && "bg-overdye-tint text-overdye-ink",
                    l.kind === "removed" && "bg-failed-tint text-destructive"
                  )}
                >
                  <span aria-hidden className="mr-3 opacity-50 select-none">
                    {l.kind === "added"
                      ? "+"
                      : l.kind === "removed"
                        ? "-"
                        : " "}
                  </span>
                  {l.text}
                </div>
              ))
            )
          )}
        </pre>
      )}
    </section>
  )
}

function Field({
  label,
  htmlFor,
  help,
  invalid = false,
  children,
}: {
  label: string
  htmlFor?: string
  help?: string
  invalid?: boolean
  children: ReactNode
}) {
  return (
    <div className="grid min-w-0 content-start gap-1.5">
      {htmlFor ? (
        <Label htmlFor={htmlFor}>{label}</Label>
      ) : (
        <span className="text-[13px] leading-none font-medium">{label}</span>
      )}
      {children}
      {help && (
        <span
          className={cn(
            "text-xs",
            invalid ? "text-destructive" : "text-muted-foreground"
          )}
        >
          {help}
        </span>
      )}
    </div>
  )
}

function Empty({
  title,
  action,
  children,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="grid justify-items-start gap-2 rounded-xl border border-dashed px-4 py-5">
      <p className="text-[13px] font-medium">{title}</p>
      <p className="max-w-[60ch] text-[13px] text-muted-foreground">
        {children}
      </p>
      {action && <div className="flex flex-wrap gap-2 pt-1">{action}</div>}
    </div>
  )
}
