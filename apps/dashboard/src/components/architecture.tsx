import { CheckIcon, ExternalLinkIcon, GitBranchIcon } from "lucide-react"
import { type FormEvent, type ReactNode, useEffect, useState } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAnalysesCreate,
  useAnalysesGraph,
  useAnalysesList,
  useProjectsUpdate,
} from "@/client/forge/loomplane/hooks"
import type {
  Analysis,
  Evidence,
  Graph,
  Project,
  Service,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery, useRefetchWhile } from "@/client/hooks"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
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
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
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
  commitURL,
  connections,
  ecosystemLabel,
  evidenceLabel,
  evidenceURL,
  isActive,
  kindLabel,
  latestSucceeded,
  resourceLabel,
  shortSHA,
  stepIndex,
  stepLabel,
  steps,
} from "@/lib/architecture"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"

// After this long in the queue, the likeliest reason is that no worker is
// running, so the card says so.
const workerHintAfter = 30_000

/**
 * Architecture is what Loomplane found in a project's repository: the
 * services it builds, the packages they share and the resources they
 * need, each with the lines it was read from.
 */
export function Architecture({ project }: { project: Project }) {
  const { can } = useOrganization()
  const analyses = useQuery(useAnalysesList, { path: { id: project.id } })
  const list = analyses.data?.analyses ?? []
  const latest = list[0]
  const running = !!latest && isActive(latest.status)
  const shown = latestSucceeded(list)
  const mayRun = can("analysis:run")

  useRefetchWhile(running, analyses.refetch)

  return (
    <Card>
      <CardHeader>
        <CardTitle>Architecture</CardTitle>
        <CardDescription>
          The services in this project&apos;s repository, what they share and
          what they need, read from its manifests and code. Nothing in the
          repository is run.
        </CardDescription>
        {project.repositoryUrl && mayRun && (
          <CardAction>
            <Analyze project={project} running={running} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {!project.repositoryUrl && (
          <ConnectRepository
            project={project}
            mayEdit={can("project:update")}
          />
        )}
        {analyses.status === "error" && (
          <Alert variant="destructive">
            <AlertDescription>
              {messageOf(analyses.error, "Analyses couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        )}
        {project.repositoryUrl &&
          !analyses.data &&
          analyses.status !== "error" && <Skeleton className="h-24 w-full" />}
        {project.repositoryUrl && analyses.data && !latest && (
          <Empty className="border">
            <EmptyHeader>
              <EmptyTitle>Not analyzed yet</EmptyTitle>
              <EmptyDescription>
                {mayRun
                  ? "Analyze the repository to see the services Loomplane can deploy from it."
                  : "Someone with the developer role or above can analyze it."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        )}
        {latest && running && <Progress analysis={latest} />}
        {latest?.status === "failed" && (
          <Alert variant="destructive">
            <AlertDescription>
              The latest analysis
              {latest.commitSha ? ` of ${shortSHA(latest.commitSha)}` : ""}{" "}
              failed: {latest.error}
            </AlertDescription>
          </Alert>
        )}
        {shown && <GraphView analysis={shown} />}
        {list.length > 1 && <History analyses={list} />}
      </CardContent>
    </Card>
  )
}

function Analyze({ project, running }: { project: Project; running: boolean }) {
  const start = useMutation(useAnalysesCreate)
  const [choosing, setChoosing] = useState(false)
  const [ref, setRef] = useState("")

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
    <div className="flex items-center gap-2">
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setChoosing(true)}
        disabled={running || start.isPending}
      >
        <GitBranchIcon />
        Another branch
      </Button>
      <Button
        size="sm"
        onClick={() => void run("")}
        disabled={running || start.isPending}
      >
        {(running || start.isPending) && <Spinner />}
        {running ? "Analyzing" : "Analyze"}
      </Button>
      <Dialog open={choosing} onOpenChange={setChoosing}>
        <DialogContent className="sm:max-w-md">
          <form onSubmit={submit}>
            <DialogHeader>
              <DialogTitle>Analyze a branch, tag or commit</DialogTitle>
              <DialogDescription>
                Loomplane analyzes the exact commit the name points at now.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup className="py-4">
              <Field>
                <FieldLabel htmlFor="analysis-ref">Ref</FieldLabel>
                <Input
                  id="analysis-ref"
                  value={ref}
                  onChange={(e) => setRef(e.target.value)}
                  placeholder="main, v1.4.0 or a commit SHA"
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
    </div>
  )
}

function ConnectRepository({
  project,
  mayEdit,
}: {
  project: Project
  mayEdit: boolean
}) {
  const update = useMutation(useProjectsUpdate)
  const [url, setURL] = useState("")

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await update.mutateAsync({
        path: { id: project.id },
        body: { repositoryUrl: url.trim() },
      })
      toast.success("Repository added")
    } catch (err) {
      toast.error(messageOf(err, "The repository wasn't added."))
    }
  }

  if (!mayEdit) {
    return (
      <p className="text-sm text-muted-foreground">
        This project has no repository yet. Someone with the developer role or
        above can add one.
      </p>
    )
  }
  return (
    <form onSubmit={(e) => void submit(e)} className="max-w-xl">
      <Field>
        <FieldLabel htmlFor="repository-url">GitHub repository</FieldLabel>
        <div className="flex gap-2">
          <Input
            id="repository-url"
            value={url}
            onChange={(e) => setURL(e.target.value)}
            placeholder="https://github.com/acme/monorepo"
            required
          />
          <Button type="submit" disabled={update.isPending || !url.trim()}>
            {update.isPending && <Spinner />}
            Add
          </Button>
        </div>
        <FieldDescription>
          Public repositories work as they are. Private ones need the GitHub
          connection.
        </FieldDescription>
      </Field>
    </form>
  )
}

function Progress({ analysis }: { analysis: Analysis }) {
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
    <div className="flex flex-col gap-3" role="status" aria-live="polite">
      <ol className="flex flex-col gap-2 text-sm sm:flex-row sm:gap-6">
        {steps.map((step, i) => (
          <li
            key={step}
            className={cn(
              "flex items-center gap-2",
              i > at && "text-muted-foreground"
            )}
          >
            {i < at && <CheckIcon className="size-4 text-overdye" />}
            {i === at && <Spinner />}
            {i > at && <span className="size-4" aria-hidden="true" />}
            {stepLabel(step)}
          </li>
        ))}
      </ol>
      {stuck && (
        <p className="text-sm text-muted-foreground">
          No worker has picked this up yet. Analyses run in loomplane-worker, so
          check that it&apos;s running.
        </p>
      )}
    </div>
  )
}

function GraphView({ analysis }: { analysis: Analysis }) {
  const graph = useQuery(useAnalysesGraph, { path: { id: analysis.id } })
  const [open, setOpen] = useState<Service | null>(null)

  if (graph.status === "error") {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(graph.error, "The analysis couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  }
  if (!graph.data) return <Skeleton className="h-48 w-full" />
  const g = graph.data.graph
  const s = analysis.summary

  return (
    <div className="flex flex-col gap-6">
      <p className="text-sm text-muted-foreground">
        Read from{" "}
        <a
          className="font-mono text-foreground underline-offset-4 hover:underline"
          href={commitURL(analysis.repository, analysis.commitSha)}
          target="_blank"
          rel="noreferrer"
        >
          {analysis.repository}@{shortSHA(analysis.commitSha)}
        </a>
        {analysis.ref ? ` (${analysis.ref})` : ""}
        {analysis.finishedAt ? `, ${formatDate(analysis.finishedAt)}.` : "."}
      </p>

      <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <Stat label="Services" value={s.services} />
        <Stat label="Deployable" value={s.deployable} />
        <Stat label="Shared packages" value={s.packages} />
        <Stat
          label="Resources"
          value={s.resources}
          note={
            s.needsConfirmation
              ? `${s.needsConfirmation} to confirm`
              : undefined
          }
        />
      </dl>

      {g.services.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No services found. Loomplane reads Go, Rust and Node manifests,
          Dockerfiles and Compose files; this commit has none it recognizes as
          something that runs.
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg border">
          <Table>
            <TableHeader className="bg-muted">
              <TableRow>
                <TableHead>Service</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead className="hidden md:table-cell">
                  Built with
                </TableHead>
                <TableHead className="hidden lg:table-cell">Needs</TableHead>
                <TableHead>Confidence</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {g.services.map((svc) => (
                <ServiceRow
                  key={svc.id}
                  graph={g}
                  service={svc}
                  onOpen={() => setOpen(svc)}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {g.resources.length > 0 && (
        <section className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">Resources</h3>
          <ul className="flex flex-col gap-2">
            {g.resources.map((r) => (
              <li key={r.id} className="rounded-lg border p-3 text-sm">
                <details>
                  <summary className="flex cursor-pointer list-none flex-wrap items-center gap-2">
                    <span className="font-medium">{resourceLabel(r.kind)}</span>
                    <ConfidenceBadge confidence={r.confidence} />
                    {r.needsConfirmation && (
                      <Badge variant="outline">
                        Confirm before provisioning
                      </Badge>
                    )}
                    <span className="text-muted-foreground">
                      {r.evidence.length === 1
                        ? "1 piece of evidence"
                        : `${r.evidence.length} pieces of evidence`}
                    </span>
                  </summary>
                  <EvidenceList
                    analysis={analysis}
                    evidence={r.evidence}
                    className="mt-3"
                  />
                </details>
              </li>
            ))}
          </ul>
        </section>
      )}

      <Structure analysis={analysis} graph={g} />

      <Sheet open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="w-full overflow-y-auto sm:max-w-lg">
          {open && (
            <ServiceDetail analysis={analysis} graph={g} service={open} />
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function Stat({
  label,
  value,
  note,
}: {
  label: string
  value: number
  note?: string
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="text-2xl font-semibold tabular-nums">{value}</dd>
      {note && <dd className="text-xs text-muted-foreground">{note}</dd>}
    </div>
  )
}

function ServiceRow({
  graph,
  service,
  onOpen,
}: {
  graph: Graph
  service: Service
  onOpen: () => void
}) {
  const { needs } = connections(graph, service.id)
  return (
    <TableRow>
      <TableCell>
        <button
          type="button"
          onClick={onOpen}
          className="text-left font-medium underline-offset-4 hover:underline focus-visible:underline focus-visible:outline-none"
        >
          {service.name}
        </button>
        <div className="font-mono text-xs text-muted-foreground">
          {service.path}
        </div>
      </TableCell>
      <TableCell>
        <span className={cn(!service.deployable && "text-muted-foreground")}>
          {kindLabel(service.kind)}
        </span>
      </TableCell>
      <TableCell className="hidden md:table-cell">
        {[ecosystemLabel(service.ecosystem), service.framework]
          .filter(Boolean)
          .join(", ")}
      </TableCell>
      <TableCell className="hidden lg:table-cell">
        {needs.length > 0
          ? needs.map((n) => resourceLabel(n.resource.kind)).join(", ")
          : "Nothing found"}
      </TableCell>
      <TableCell>
        <ConfidenceBadge confidence={service.confidence} />
      </TableCell>
    </TableRow>
  )
}

function ServiceDetail({
  analysis,
  graph,
  service,
}: {
  analysis: Analysis
  graph: Graph
  service: Service
}) {
  const { uses, needs } = connections(graph, service.id)
  return (
    <>
      <SheetHeader>
        <SheetTitle>{service.name}</SheetTitle>
        <SheetDescription>
          {kindLabel(service.kind)}
          {service.deployable ? "" : ", which Loomplane doesn't deploy"}
        </SheetDescription>
      </SheetHeader>
      <div className="flex flex-col gap-6 px-4 pb-6 text-sm">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
          <Fact label="Built with">
            {[ecosystemLabel(service.ecosystem), service.framework]
              .filter(Boolean)
              .join(", ")}
          </Fact>
          {service.runtime && <Fact label="Runtime">{service.runtime}</Fact>}
          <Fact label="Source">
            <code className="font-mono wrap-anywhere">{service.path}</code>
          </Fact>
          <Fact label="Entrypoint">
            <code className="font-mono wrap-anywhere">
              {service.entrypoint}
            </code>
          </Fact>
          {service.image && (
            <Fact label="Dockerfile">
              <code className="font-mono wrap-anywhere">{service.image}</code>
            </Fact>
          )}
          {service.ports && service.ports.length > 0 && (
            <Fact label="Ports">{service.ports.join(", ")}</Fact>
          )}
          {service.commands.build && (
            <Fact label="Build">
              <code className="font-mono wrap-anywhere">
                {service.commands.build}
              </code>
            </Fact>
          )}
          {service.commands.start && (
            <Fact label="Start">
              <code className="font-mono wrap-anywhere">
                {service.commands.start}
              </code>
            </Fact>
          )}
          <Fact label="Confidence">
            <ConfidenceBadge confidence={service.confidence} />
          </Fact>
        </dl>

        {uses.length > 0 && (
          <section className="flex flex-col gap-2">
            <h3 className="font-medium">Uses</h3>
            <ul className="flex flex-col gap-1">
              {uses.map((p) => (
                <li key={p.id}>
                  {p.name}{" "}
                  <span className="font-mono text-xs text-muted-foreground">
                    {p.path}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        )}
        {needs.length > 0 && (
          <section className="flex flex-col gap-2">
            <h3 className="font-medium">Needs</h3>
            <ul className="flex flex-col gap-1">
              {needs.map((n) => (
                <li
                  key={n.resource.id}
                  className="flex flex-wrap items-center gap-2"
                >
                  {resourceLabel(n.resource.kind)}
                  <ConfidenceBadge confidence={n.confidence} />
                  {n.via && (
                    <span className="text-muted-foreground">
                      through {n.via}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}

        <section className="flex flex-col gap-2">
          <h3 className="font-medium">Evidence</h3>
          <EvidenceList analysis={analysis} evidence={service.evidence} />
        </section>
      </div>
    </>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </>
  )
}

function EvidenceList({
  analysis,
  evidence,
  className,
}: {
  analysis: Analysis
  evidence: Evidence[]
  className?: string
}) {
  return (
    <ul className={cn("flex flex-col gap-2", className)}>
      {evidence.map((ev, i) => (
        <li key={`${ev.path}:${ev.line ?? 0}:${i}`}>
          <a
            href={evidenceURL(analysis.repository, analysis.commitSha, ev)}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 font-mono text-xs underline-offset-4 hover:underline"
          >
            {evidenceLabel(ev)}
            <ExternalLinkIcon className="size-3" aria-hidden="true" />
          </a>
          {ev.detail && (
            <pre className="mt-1 overflow-x-auto rounded bg-muted px-2 py-1 font-mono text-xs whitespace-pre-wrap">
              {ev.detail}
            </pre>
          )}
        </li>
      ))}
    </ul>
  )
}

function ConfidenceBadge({ confidence }: { confidence: string }) {
  const label =
    confidence === "certain"
      ? "Certain"
      : confidence === "likely"
        ? "Likely"
        : "Possible"
  return (
    <Badge
      variant={confidence === "possible" ? "outline" : "secondary"}
      className={cn(confidence === "possible" && "text-muted-foreground")}
    >
      {label}
    </Badge>
  )
}

function Structure({ analysis, graph }: { analysis: Analysis; graph: Graph }) {
  const tooling = [
    ...graph.workspaces.map(
      (w) =>
        `${w.tool} workspace at ${w.path === "." ? "the root" : w.path} (${w.members.length})`
    ),
    ...graph.buildSystems.map((b) => b.name),
  ]
  return (
    <div className="flex flex-col gap-2 text-sm">
      {tooling.length > 0 && (
        <p className="text-muted-foreground">
          Monorepo tooling: {tooling.join(", ")}.
        </p>
      )}
      {graph.packages.length > 0 && (
        <details>
          <summary className="cursor-pointer font-medium">
            Shared packages ({graph.packages.length})
          </summary>
          <ul className="mt-2 flex flex-col gap-1">
            {graph.packages.map((p) => (
              <li key={p.id}>
                {p.name}{" "}
                <span className="text-muted-foreground">
                  {ecosystemLabel(p.ecosystem)}
                </span>{" "}
                <a
                  className="font-mono text-xs text-muted-foreground underline-offset-4 hover:underline"
                  href={evidenceURL(analysis.repository, analysis.commitSha, {
                    path: p.path,
                    detail: "",
                  })}
                  target="_blank"
                  rel="noreferrer"
                >
                  {p.path}
                </a>
              </li>
            ))}
          </ul>
        </details>
      )}
      {graph.notes.length > 0 && (
        <details>
          <summary className="cursor-pointer font-medium">
            Skipped ({graph.notes.length})
          </summary>
          <ul className="mt-2 flex flex-col gap-1 text-muted-foreground">
            {graph.notes.map((n, i) => (
              <li key={`${n.path ?? ""}:${i}`}>
                {n.path && <code className="font-mono">{n.path}</code>}{" "}
                {n.message}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

function History({ analyses }: { analyses: Analysis[] }) {
  return (
    <details className="text-sm">
      <summary className="cursor-pointer font-medium">
        Earlier analyses ({analyses.length - 1})
      </summary>
      <ul className="mt-2 flex flex-col gap-1">
        {analyses.slice(1).map((a) => (
          <li key={a.id} className="flex flex-wrap items-center gap-2">
            <time dateTime={a.createdAt}>{formatDate(a.createdAt)}</time>
            <span className="text-muted-foreground">
              {a.ref || "default branch"}
            </span>
            {a.commitSha && (
              <code className="font-mono text-xs">{shortSHA(a.commitSha)}</code>
            )}
            <span
              className={cn(
                a.status === "failed" && "text-destructive",
                isActive(a.status) && "text-muted-foreground"
              )}
            >
              {a.status === "succeeded"
                ? `${a.summary.services} services`
                : a.status}
            </span>
          </li>
        ))}
      </ul>
    </details>
  )
}
