// What a repository analysis found, in the pieces the project page lays
// out: the controls that start one, its progress, the services table, the
// detail sheets with the lines each finding was read from, and the history.
import {
  ExternalLinkIcon,
  FolderGit2Icon,
  GitBranchIcon,
  ScanSearchIcon,
} from "lucide-react"
import {
  type CSSProperties,
  type FormEvent,
  type ReactNode,
  useEffect,
  useState,
} from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesCreate,
  useAnalysesList,
  useProjectsGet,
  useProjectsList,
  useSourcesCreate,
  useSourcesList,
} from "@/client/forge/loomplane/hooks"
import type {
  Analysis,
  Evidence,
  Graph,
  Project,
  Resource,
  Service,
} from "@/client/forge/loomplane/types"
import {
  useInvalidate,
  useMutation,
  useQuery,
  useRefetchWhile,
} from "@/client/hooks"
import {
  type GraphSelection,
  needsReview,
  ResourceGlyph,
} from "@/components/service-graph"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
  InputGroupText,
} from "@/components/ui/input-group"
import {
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Spinner } from "@/components/ui/spinner"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  commitURL,
  connections,
  ecosystemLabel,
  evidenceLabel,
  isActive,
  kindLabel,
  latestBySource,
  latestSucceeded,
  linkTo,
  resourceLabel,
  shortSHA,
  type SourceLink,
  stepIndex,
  stepLabel,
  steps,
} from "@/lib/architecture"
import { formatDate, githubRepo, repositoriesLabel } from "@/lib/format"
import { cn } from "@/lib/utils"

// After this long in the queue, the likeliest reason is that no worker is
// running, so the banner says so.
const workerHintAfter = 30_000

/**
 * useAnalyses is a project's analyses, newest first, watched while one
 * runs: the latest, whether any source's is running, and the newest that
 * produced a graph, which is the one the page shows.
 */
export function useAnalyses(projectID: string) {
  const analyses = useQuery(useAnalysesList, { path: { id: projectID } })
  const list = analyses.data?.analyses ?? []
  const latest = list[0]
  const running = [...latestBySource(list).values()].some((a) =>
    isActive(a.status)
  )
  useRefetchWhile(running, analyses.refetch)
  return {
    list,
    loaded: !!analyses.data,
    error: analyses.status === "error" ? analyses.error : undefined,
    latest,
    running,
    shown: latestSucceeded(list),
  }
}

/** AnalyzeActions start an analysis of the default branch or another ref. */
export function AnalyzeActions({
  project,
  running,
}: {
  project: Project
  running: boolean
}) {
  const start = useMutation(useAnalysesCreate)
  const sources = useQuery(useSourcesList, { path: { id: project.id } })
  // The API refuses a ref when a project has several sources, so the choice
  // is only offered for one.
  const single = sources.data?.sources.filter((s) => !s.removedAt).length === 1
  const [choosing, setChoosing] = useState(false)
  const [ref, setRef] = useState("")
  const busy = running || start.isPending

  const run = async (target: string) => {
    try {
      await start.mutateAsync({
        path: { id: project.id },
        body: target ? { ref: target } : {},
      })
      setChoosing(false)
      setRef("")
    } catch (err) {
      toast.error(messageOf(err, "The analysis didn't start."))
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    void run(ref.trim())
  }

  return (
    <>
      {single && (
        <Button
          variant="outline"
          className="rounded-full"
          onClick={() => setChoosing(true)}
          disabled={busy}
        >
          <GitBranchIcon />
          Another branch
        </Button>
      )}
      <Button
        className="rounded-full"
        onClick={() => void run("")}
        disabled={busy}
      >
        {busy ? <Spinner /> : <ScanSearchIcon />}
        {running ? "Analyzing" : "Analyze"}
      </Button>
      <Dialog open={choosing} onOpenChange={setChoosing}>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={submit} className="grid gap-5">
            <DialogHeader>
              <DialogTitle>Analyze a branch, tag or commit</DialogTitle>
              <DialogDescription>
                Loomplane analyzes the exact commit the name points at now.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field>
                <FieldLabel htmlFor="analysis-ref">Ref</FieldLabel>
                <Input
                  id="analysis-ref"
                  value={ref}
                  onChange={(e) => setRef(e.target.value)}
                  placeholder="main, v1.4.0 or a commit SHA"
                  className="font-mono"
                  autoFocus
                  required
                />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setChoosing(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={start.isPending || !ref.trim()}>
                {start.isPending && <Spinner />}
                Analyze
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}

/** AnalysisBanner reports an analysis that's running or just failed. */
export function AnalysisBanner({ analysis }: { analysis: Analysis }) {
  if (isActive(analysis.status)) return <Running analysis={analysis} />
  if (analysis.status !== "failed") return null
  return (
    <Alert variant="destructive" className="rounded-xl">
      <AlertTitle>
        The latest analysis
        {analysis.commitSha ? ` of ${shortSHA(analysis.commitSha)}` : ""} failed
      </AlertTitle>
      <AlertDescription>{analysis.error}</AlertDescription>
    </Alert>
  )
}

function Running({ analysis }: { analysis: Analysis }) {
  const at = stepIndex(analysis.status)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 5_000)
    return () => window.clearInterval(id)
  }, [])
  const stuck =
    analysis.status === "queued" &&
    now - new Date(analysis.createdAt).getTime() > workerHintAfter

  return (
    <Card
      role="status"
      aria-live="polite"
      className="loom-glow gap-3 px-5 py-4"
      style={
        { "--glow-x": `${((at + 0.5) / steps.length) * 100}%` } as CSSProperties
      }
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-sm font-medium">
          Analyzing {analysis.ref || "the default branch"}
        </span>
        <span className="text-xs text-muted-foreground">
          {stepLabel(analysis.status)}
        </span>
      </div>
      <ol className="grid grid-cols-3 gap-1.5" aria-label="Progress">
        {steps.map((step, i) => (
          <li key={step} className="grid gap-1.5">
            <span
              className={cn(
                "h-1.5 rounded-full",
                i < at && "bg-overdye",
                i === at &&
                  "thread-shuttle shuttle-glow bg-weld motion-reduce:shadow-none",
                i > at && "bg-warp"
              )}
            />
            <span
              className={cn(
                "text-xs",
                i > at ? "text-muted-foreground" : "text-foreground"
              )}
            >
              {stepLabel(step)}
            </span>
          </li>
        ))}
      </ol>
      {stuck && (
        <p className="text-xs text-muted-foreground">
          No worker has picked this up yet. Analyses run in loomplane-worker, so
          check that it&apos;s running.
        </p>
      )}
    </Card>
  )
}

/** ConnectRepository asks for the repository a project builds from. */
export function ConnectRepository({
  project,
  mayEdit,
}: {
  project: Project
  mayEdit: boolean
}) {
  const add = useMutation(useSourcesCreate)
  const invalidate = useInvalidate()
  const [repo, setRepo] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await add.mutateAsync({
        path: { id: project.id },
        body: { repository: repo.trim() },
      })
      // The project's own repositories changed; the page reads them from it.
      invalidate(useProjectsGet, { path: { id: project.id } })
      invalidate(useProjectsList)
      toast.success("Repository added", {
        description: "Analyze it to see the services Loomplane can deploy.",
      })
    } catch (err) {
      toast.error(messageOf(err, "The repository wasn't added."))
    }
  }

  return (
    <Empty className="border border-dashed">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <FolderGit2Icon />
        </EmptyMedia>
        <EmptyTitle>No repository yet</EmptyTitle>
        <EmptyDescription>
          {mayEdit
            ? "Add the GitHub repository this project builds from. Public ones work as they are; private ones need the GitHub connection."
            : "Someone with the developer role or above can add the repository this project builds from."}
        </EmptyDescription>
      </EmptyHeader>
      {mayEdit && (
        <EmptyContent>
          <form onSubmit={(e) => void submit(e)} className="w-full max-w-md">
            <InputGroup>
              <InputGroupAddon>
                <InputGroupText>github.com/</InputGroupText>
              </InputGroupAddon>
              <InputGroupInput
                aria-label="Repository"
                value={repo}
                onChange={(e) => {
                  const v = e.target.value
                  setRepo(githubRepo(v) || v.trim())
                }}
                placeholder="acme/storefront"
                className="font-mono text-sm"
                spellCheck={false}
                required
              />
              <InputGroupAddon align="inline-end">
                <InputGroupButton
                  type="submit"
                  variant="default"
                  disabled={add.isPending || !repo.trim()}
                >
                  {add.isPending && <Spinner />}
                  Add
                </InputGroupButton>
              </InputGroupAddon>
            </InputGroup>
          </form>
        </EmptyContent>
      )}
    </Empty>
  )
}

/** NotAnalyzed is a project's graph before its first analysis. */
export function NotAnalyzed({
  project,
  mayRun,
  running,
}: {
  project: Project
  mayRun: boolean
  running: boolean
}) {
  const repo = repositoriesLabel(project.repositories)
  return (
    <Empty className="border border-dashed">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <ScanSearchIcon />
        </EmptyMedia>
        <EmptyTitle>
          {running ? "Analyzing" : "Nothing discovered yet"}
        </EmptyTitle>
        <EmptyDescription>
          {running
            ? `Loomplane is reading ${repo}. The services and resources it finds show up here.`
            : mayRun
              ? `Analyze ${repo} and Loomplane maps the services and resources it finds here. Nothing in the repository is run.`
              : "Someone with the developer role or above can analyze it."}
        </EmptyDescription>
      </EmptyHeader>
      {mayRun && !running && (
        <EmptyContent>
          <AnalyzeOnce project={project} />
        </EmptyContent>
      )}
    </Empty>
  )
}

function AnalyzeOnce({ project }: { project: Project }) {
  const start = useMutation(useAnalysesCreate)
  return (
    <Button
      className="rounded-full"
      disabled={start.isPending}
      onClick={() =>
        start
          .mutateAsync({ path: { id: project.id }, body: {} })
          .catch((err: unknown) =>
            toast.error(messageOf(err, "The analysis didn't start."))
          )
      }
    >
      {start.isPending ? <Spinner /> : <ScanSearchIcon />}
      Analyze the repository
    </Button>
  )
}

/** GraphCaption says which commit of each repository the graph was read from. */
export function GraphCaption({ graph }: { graph: Graph }) {
  const read = (graph.sources ?? []).filter((s) => s.commitSha)
  return (
    <p className="text-xs text-muted-foreground">
      Read from{" "}
      {read.map((s, i) => (
        <span key={s.alias}>
          {i > 0 && ", "}
          <a
            className="font-mono text-foreground underline-offset-4 hover:underline"
            href={commitURL(s.repository, s.commitSha)}
            target="_blank"
            rel="noreferrer"
          >
            {read.length > 1 ? `${s.alias}@` : `${s.repository}@`}
            {shortSHA(s.commitSha)}
          </a>
        </span>
      ))}
      . Hover a node to trace what it needs, click it for the evidence. Dashed
      links lead to resources nobody has confirmed yet.
    </p>
  )
}

/** ServicesTable lists what the analysis found that runs. */
export function ServicesTable({
  graph,
  onSelect,
}: {
  graph: Graph
  onSelect: (s: GraphSelection) => void
}) {
  return (
    <Card className="py-0">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Service</TableHead>
            <TableHead>Kind</TableHead>
            <TableHead className="hidden md:table-cell">Built with</TableHead>
            <TableHead className="hidden lg:table-cell">Needs</TableHead>
            <TableHead>Detected</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {graph.services.map((s) => {
            const { needs } = connections(graph, s.id)
            return (
              <TableRow
                key={s.id}
                className="cursor-pointer"
                onClick={() => onSelect({ kind: "service", id: s.id })}
              >
                <TableCell>
                  <div className="grid">
                    <button
                      type="button"
                      className="justify-self-start text-left font-medium underline-offset-4 hover:underline focus-visible:underline focus-visible:outline-none"
                      onClick={(e) => {
                        e.stopPropagation()
                        onSelect({ kind: "service", id: s.id })
                      }}
                    >
                      {s.name}
                    </button>
                    <code className="font-mono text-xs text-muted-foreground">
                      {s.path}
                    </code>
                  </div>
                </TableCell>
                <TableCell>
                  <span
                    className={cn(!s.deployable && "text-muted-foreground")}
                  >
                    {kindLabel(s.kind)}
                  </span>
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  {[ecosystemLabel(s.ecosystem), s.framework]
                    .filter(Boolean)
                    .join(", ")}
                </TableCell>
                <TableCell className="hidden text-muted-foreground lg:table-cell">
                  {needs.length > 0
                    ? needs
                        .map((n) => resourceLabel(n.resource.kind))
                        .join(", ")
                    : "Nothing found"}
                </TableCell>
                <TableCell>
                  <ConfidenceBadge
                    confidence={s.confidence}
                    hint={
                      s.evidence[0] ? evidenceLabel(s.evidence[0]) : undefined
                    }
                  />
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      {graph.services.length === 0 && (
        <p className="border-t px-4 py-8 text-center text-sm text-muted-foreground">
          No services found. Loomplane reads Go, Rust and Node manifests,
          Dockerfiles and Compose files; this commit has none it recognizes as
          something that runs.
        </p>
      )}
    </Card>
  )
}

/** Structure is the monorepo around the services: tooling, packages, skips. */
export function Structure({
  sources,
  graph,
}: {
  sources: SourceLink[]
  graph: Graph
}) {
  const tooling = [
    ...graph.workspaces.map(
      (w) =>
        `${w.tool} workspace at ${w.path === "." ? "the root" : w.path} (${w.members.length})`
    ),
    ...graph.buildSystems.map((b) => b.name),
  ]
  if (
    tooling.length === 0 &&
    graph.packages.length === 0 &&
    graph.notes.length === 0
  ) {
    return null
  }
  return (
    <div className="grid gap-3 text-sm">
      {tooling.length > 0 && (
        <p className="text-muted-foreground">
          Monorepo tooling: {tooling.join(", ")}.
        </p>
      )}
      {graph.packages.length > 0 && (
        <details className="group rounded-xl border px-4 py-3">
          <summary className="cursor-pointer font-medium">
            Shared packages{" "}
            <span className="text-muted-foreground tabular-nums">
              {graph.packages.length}
            </span>
          </summary>
          <ul className="mt-3 grid gap-1.5">
            {graph.packages.map((p) => {
              const href = linkTo(sources, {
                path: p.path,
                detail: "",
                source: p.source,
              })
              return (
                <li key={p.id} className="flex flex-wrap items-baseline gap-2">
                  <span>{p.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {ecosystemLabel(p.ecosystem)}
                  </span>
                  {href ? (
                    <a
                      className="font-mono text-xs text-muted-foreground underline-offset-4 hover:underline"
                      href={href}
                      target="_blank"
                      rel="noreferrer"
                    >
                      {p.path}
                    </a>
                  ) : (
                    <code className="font-mono text-xs text-muted-foreground">
                      {p.path}
                    </code>
                  )}
                </li>
              )
            })}
          </ul>
        </details>
      )}
      {graph.notes.length > 0 && (
        <details className="rounded-xl border px-4 py-3">
          <summary className="cursor-pointer font-medium">
            Skipped{" "}
            <span className="text-muted-foreground tabular-nums">
              {graph.notes.length}
            </span>
          </summary>
          <ul className="mt-3 grid gap-1.5 text-muted-foreground">
            {graph.notes.map((n, i) => (
              <li key={`${n.path ?? ""}:${i}`}>
                {n.path && <code className="font-mono text-xs">{n.path}</code>}{" "}
                {n.message}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

/** AnalysisHistory lists every analysis of the project, newest first. */
export function AnalysisHistory({ analyses }: { analyses: Analysis[] }) {
  if (analyses.length === 0) {
    return (
      <Card className="py-0">
        <p className="px-4 py-8 text-center text-sm text-muted-foreground">
          No analyses yet.
        </p>
      </Card>
    )
  }
  return (
    <Card className="py-0">
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>Started</TableHead>
            <TableHead>Ref</TableHead>
            <TableHead>Commit</TableHead>
            <TableHead>Result</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {analyses.map((a) => (
            <TableRow key={a.id}>
              <TableCell className="text-muted-foreground">
                <time dateTime={a.createdAt}>{formatDate(a.createdAt)}</time>
              </TableCell>
              <TableCell className="font-mono text-xs">
                {a.ref || (
                  <span className="font-sans text-muted-foreground">
                    default branch
                  </span>
                )}
              </TableCell>
              <TableCell>
                {a.commitSha ? (
                  <a
                    href={commitURL(a.repository, a.commitSha)}
                    target="_blank"
                    rel="noreferrer"
                    className="font-mono text-xs underline-offset-4 hover:underline"
                  >
                    {shortSHA(a.commitSha)}
                  </a>
                ) : (
                  <span className="text-muted-foreground">Not fetched</span>
                )}
              </TableCell>
              <TableCell>
                <span className="inline-flex items-center gap-2">
                  <span
                    aria-hidden="true"
                    className={cn(
                      "inline-block h-1.5 w-3 shrink-0 rounded-full",
                      a.status === "succeeded" && "bg-overdye",
                      a.status === "failed" && "bg-destructive",
                      isActive(a.status) &&
                        "animate-pulse bg-weld motion-reduce:animate-none"
                    )}
                  />
                  {a.status === "succeeded"
                    ? `${a.summary.services} services, ${a.summary.resources} resources`
                    : a.status === "failed"
                      ? "Failed"
                      : stepLabel(a.status)}
                </span>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  )
}

/** ConfidenceBadge says how sure an analysis is about a finding. */
export function ConfidenceBadge({
  confidence,
  hint,
}: {
  confidence: string
  hint?: string
}) {
  const label =
    confidence === "certain"
      ? "Certain"
      : confidence === "likely"
        ? "Likely"
        : "Possible"
  const badge = (
    <Badge
      variant="outline"
      className={cn(
        "font-normal",
        confidence === "possible" && "border-dashed text-muted-foreground"
      )}
    >
      {label}
    </Badge>
  )
  if (!hint) return badge
  return (
    <Tooltip>
      <TooltipTrigger render={<span className="inline-flex" />}>
        {badge}
      </TooltipTrigger>
      <TooltipContent className="font-mono">{hint}</TooltipContent>
    </Tooltip>
  )
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 wrap-anywhere">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function DetailSection({
  title,
  children,
}: {
  title: string
  children: ReactNode
}) {
  return (
    <section className="grid gap-3 px-4 py-4">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
}

function Chips({
  items,
  onSelect,
}: {
  items: {
    id: string
    label: string
    selection?: GraphSelection
    note?: string
    /** from names the repository a package comes from, when it isn't the service's own. */
    from?: { alias: string; dashed: boolean }
  }[]
  onSelect: (s: GraphSelection) => void
}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {items.map((it) =>
        it.selection ? (
          <Button
            key={it.id}
            variant="outline"
            size="xs"
            className="font-normal"
            onClick={() => it.selection && onSelect(it.selection)}
          >
            {it.label}
            {it.note && (
              <span className="text-muted-foreground">{it.note}</span>
            )}
          </Button>
        ) : (
          <Badge key={it.id} variant="secondary" className="font-normal">
            {it.label}
            {it.from && (
              <Badge
                variant="outline"
                className={cn(
                  "h-4 px-1.5 text-[0.65rem] font-normal",
                  it.from.dashed && "border-dashed text-muted-foreground"
                )}
              >
                {it.from.alias}
              </Badge>
            )}
          </Badge>
        )
      )}
    </div>
  )
}

/** ServiceDetail is the sheet for one service: how it runs and the proof. */
export function ServiceDetail({
  sources,
  graph,
  service: s,
  onSelect,
}: {
  sources: SourceLink[]
  graph: Graph
  service: Service
  onSelect: (s: GraphSelection) => void
}) {
  const { uses, needs } = connections(graph, s.id)
  const code = (v: string) => <code className="font-mono text-xs">{v}</code>
  return (
    <>
      <SheetHeader className="border-b">
        <SheetTitle>{s.name}</SheetTitle>
        <SheetDescription>
          {kindLabel(s.kind)} at <code className="font-mono">{s.path}</code>
          {s.deployable ? "" : ", which Loomplane doesn't deploy"}.
        </SheetDescription>
      </SheetHeader>
      <div className="flex-1 divide-y overflow-y-auto">
        <DetailSection title="How it runs">
          <Facts
            rows={[
              [
                "Built with",
                [ecosystemLabel(s.ecosystem), s.framework]
                  .filter(Boolean)
                  .join(", "),
              ],
              ...(s.runtime
                ? ([["Runtime", s.runtime]] as [string, ReactNode][])
                : []),
              ["Entrypoint", code(s.entrypoint)],
              ...(s.image
                ? ([["Dockerfile", code(s.image)]] as [string, ReactNode][])
                : []),
              ...(s.ports?.length
                ? ([["Ports", s.ports.join(", ")]] as [string, ReactNode][])
                : []),
              ...(s.commands.build
                ? ([["Build", code(s.commands.build)]] as [string, ReactNode][])
                : []),
              ...(s.commands.start
                ? ([["Start", code(s.commands.start)]] as [string, ReactNode][])
                : []),
              ["Confidence", <ConfidenceBadge confidence={s.confidence} />],
            ]}
          />
        </DetailSection>
        {(uses.length > 0 || needs.length > 0) && (
          <DetailSection title="Connections">
            {needs.length > 0 && (
              <div className="grid gap-1.5">
                <span className="text-sm">Needs</span>
                <Chips
                  onSelect={onSelect}
                  items={needs.map((n) => ({
                    id: n.resource.id,
                    label: resourceLabel(n.resource.kind),
                    note: n.via ? `through ${n.via}` : undefined,
                    selection: { kind: "resource", id: n.resource.id },
                  }))}
                />
              </div>
            )}
            {uses.length > 0 && (
              <div className="grid gap-1.5">
                <span className="text-sm">Uses</span>
                <Chips
                  onSelect={onSelect}
                  items={uses.map((p) => ({
                    id: p.id,
                    label: p.name,
                    // A package from another repository says which one, and
                    // a link nobody is sure of is drawn dashed.
                    from:
                      p.source && p.source !== s.source
                        ? {
                            alias: p.source,
                            dashed:
                              graph.edges.find(
                                (e) => e.from === s.id && e.to === p.id
                              )?.confidence === "likely",
                          }
                        : undefined,
                  }))}
                />
              </div>
            )}
          </DetailSection>
        )}
        <DetailSection title="How Loomplane found it">
          <EvidenceList sources={sources} evidence={s.evidence} />
        </DetailSection>
      </div>
    </>
  )
}

/** ResourceDetail is the sheet for one resource and who needs it. */
export function ResourceDetail({
  sources,
  graph,
  resource: r,
  onSelect,
}: {
  sources: SourceLink[]
  graph: Graph
  resource: Resource
  onSelect: (s: GraphSelection) => void
}) {
  const review = needsReview(r)
  const usedBy = graph.services.filter((s) =>
    connections(graph, s.id).needs.some((n) => n.resource.id === r.id)
  )
  return (
    <>
      <SheetHeader className="border-b">
        <SheetTitle className="flex items-center gap-2">
          <ResourceGlyph
            kind={r.kind}
            className="size-4 text-muted-foreground"
          />
          {resourceLabel(r.kind)}
        </SheetTitle>
        <SheetDescription>
          {review
            ? "A resource Loomplane inferred from the code. Nothing is provisioned for it until someone confirms it."
            : "A resource the code needs. Planning decides where it runs."}
        </SheetDescription>
      </SheetHeader>
      <div className="flex-1 divide-y overflow-y-auto">
        <DetailSection
          title={review ? "Why Loomplane suggests it" : "Evidence"}
        >
          <EvidenceList
            sources={sources}
            evidence={r.evidence}
            tone={review ? "weld" : "muted"}
          />
          {review && (
            <p className="text-xs text-muted-foreground">
              Low confidence, so it&apos;s a suggestion. Confirming or
              dismissing it arrives with planning.
            </p>
          )}
        </DetailSection>
        <DetailSection title="Resource">
          <Facts
            rows={[
              ["Kind", resourceLabel(r.kind)],
              ["Confidence", <ConfidenceBadge confidence={r.confidence} />],
              ["Status", review ? "Needs review" : "Found"],
            ]}
          />
        </DetailSection>
        {usedBy.length > 0 && (
          <DetailSection title="Needed by">
            <Chips
              onSelect={onSelect}
              items={usedBy.map((s) => ({
                id: s.id,
                label: s.name,
                selection: { kind: "service", id: s.id },
              }))}
            />
          </DetailSection>
        )}
      </div>
    </>
  )
}

/** EvidenceList links each finding to the line it was read from. */
export function EvidenceList({
  sources,
  evidence,
  tone = "muted",
}: {
  sources: SourceLink[]
  evidence: Evidence[]
  tone?: "muted" | "weld"
}) {
  return (
    <ul className="grid gap-2.5">
      {evidence.map((ev, i) => (
        <li key={`${ev.path}:${ev.line ?? 0}:${i}`} className="flex gap-2">
          <span
            className={cn(
              "mt-2 size-1 shrink-0 rounded-full",
              tone === "weld" ? "bg-weld" : "bg-muted-foreground"
            )}
          />
          <div className="grid min-w-0 gap-1">
            <EvidenceLink href={linkTo(sources, ev)} ev={ev} />
            {ev.detail && (
              <pre className="overflow-x-auto rounded-md bg-muted px-2 py-1 font-mono text-xs whitespace-pre-wrap">
                {ev.detail}
              </pre>
            )}
          </div>
        </li>
      ))}
    </ul>
  )
}

/** EvidenceLink is a finding's place, linked when we know which commit to link into. */
function EvidenceLink({ href, ev }: { href: string; ev: Evidence }) {
  if (!href) {
    return (
      <code className="truncate font-mono text-xs">{evidenceLabel(ev)}</code>
    )
  }
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1 font-mono text-xs underline-offset-4 hover:underline"
    >
      <span className="truncate">{evidenceLabel(ev)}</span>
      <ExternalLinkIcon className="size-3 shrink-0" aria-hidden="true" />
    </a>
  )
}
