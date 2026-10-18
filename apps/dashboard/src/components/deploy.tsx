// The Deploy tab: a project's manifest, and the plans made from it. A plan
// says what deploying an environment would do; nothing here touches a
// cloud. Approving a plan records the decision against the exact revision
// that was reviewed.
import { FileCodeIcon, RotateCcwIcon, WorkflowIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useConnectionsList,
  useDeploymentsForPlan,
  useDeploymentsList,
  useDeploymentsRetry,
  useEnvironmentConnectionsChoose,
  useManifestsCurrent,
  useManifestsDraft,
  useManifestsEdit,
  useManifestsSave,
  usePlansApprove,
  usePlansCreate,
  usePlansGet,
  usePlansList,
  usePlansReject,
  useProvidersList,
} from "@/client/forge/loomplane/hooks"
import type {
  Action,
  EditInputBody,
  Analysis,
  Deployment,
  Environment,
  Finding,
  Manifest,
  Plan,
  PlanSource,
  Project,
  Provider,
} from "@/client/forge/loomplane/types"
import {
  useInvalidate,
  useMutation,
  useQuery,
  useRefetchWhile,
} from "@/client/hooks"
import { EvidenceList } from "@/components/architecture"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
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
import { Textarea } from "@/components/ui/textarea"
import {
  commitURL,
  lockLabel,
  resourceLabel,
  shortSHA,
  type SourceLink,
} from "@/lib/architecture"
import { cloudLabel } from "@/lib/clouds"
import { formatDate } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import {
  changeCount,
  deployedCommits,
  deploymentStatus,
  isDeploying,
  isOpen,
  isPlanning,
  opLabel,
  planStatus,
  planConnections,
  scopeBody,
  stepStatus,
  steps,
  type Tone,
  withResource,
} from "@/lib/plans"
import { cn } from "@/lib/utils"
import { ManifestHistory } from "@/components/manifest-history"
import { DeploymentTargets } from "@/components/deployment-targets"
import { useDeploymentScope } from "@/lib/deployment-scope"
import {
  useDeploymentDraft,
  useDeploymentWrites,
} from "@/lib/deployment-writes"

export function DeployTab({
  project,
  environments,
  analysis,
}: {
  project: Project
  environments: Environment[]
  /** analysis is the latest succeeded analysis, if there is one. */
  analysis?: Analysis
}) {
  const [planID, setPlanID] = useState<string | null>(null)
  const [selectedEnvironment, setEnvironment] = useState("")
  const { busy, onPendingChange } = useDeploymentWrites(project.id)
  const environment = environments.some((e) => e.name === selectedEnvironment)
    ? selectedEnvironment
    : (environments[0]?.name ?? "")
  return (
    <div className="grid gap-6">
      <ManifestCard
        project={project}
        analysis={analysis}
        environments={environments}
        environment={environment}
        onEnvironment={setEnvironment}
        onPendingChange={onPendingChange}
      />
      <PlansCard
        project={project}
        environments={environments}
        environment={environment}
        onEnvironment={setEnvironment}
        busy={busy}
        onOpen={setPlanID}
      />
      <DeploymentsCard project={project} onOpen={setPlanID} />
      <Sheet
        open={!!planID}
        onOpenChange={(open) => {
          if (!open) setPlanID(null)
        }}
      >
        <SheetContent className="w-full gap-0 sm:max-w-xl">
          {planID && (
            <PlanReview id={planID} project={project} onPlanned={setPlanID} />
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function ManifestCard({
  project,
  analysis,
  environments,
  environment,
  onEnvironment,
  onPendingChange,
}: {
  project: Project
  analysis?: Analysis
  environments: Environment[]
  environment: string
  onEnvironment: (value: string) => void
  onPendingChange: (pending: boolean) => void
}) {
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  if (current.data) {
    return (
      <>
        {current.data.manifest && environment && (
          <DeploymentTargets
            projectId={project.id}
            manifest={current.data.manifest}
            environments={environments}
            environment={environment}
            onEnvironment={onEnvironment}
            onPendingChange={onPendingChange}
          />
        )}
        <details className="rounded-xl border p-3">
          <summary className="cursor-pointer text-sm font-medium">
            Edit loomplane.yaml · Revision {current.data.revision}
          </summary>
          <div className="pt-3">
            <ManifestEditor
              key={project.id}
              project={project}
              yaml={current.data.yaml}
              revision={current.data.revision}
              savedAt={current.data.createdAt}
              onPendingChange={onPendingChange}
            />
          </div>
        </details>
      </>
    )
  }
  if (current.status === "error" && statusOf(current.error) === 404) {
    if (!analysis) {
      return (
        <Empty className="border border-dashed">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <FileCodeIcon />
            </EmptyMedia>
            <EmptyTitle>No manifest yet</EmptyTitle>
            <EmptyDescription>
              The manifest says where each service runs. Analyze the repository
              first, and Loomplane drafts one from what it finds.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )
    }
    return (
      <DraftedManifest project={project} onPendingChange={onPendingChange} />
    )
  }
  if (current.status === "error") {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(current.error, "The manifest couldn't be loaded.")}
        </AlertDescription>
      </Alert>
    )
  }
  return <Skeleton className="h-80 w-full rounded-xl" />
}

function DraftedManifest({
  project,
  onPendingChange,
}: {
  project: Project
  onPendingChange: (pending: boolean) => void
}) {
  const draft = useQuery(useManifestsDraft, { path: { id: project.id } })
  if (draft.status === "error") {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          {messageOf(draft.error, "No manifest could be drafted.")}
        </AlertDescription>
      </Alert>
    )
  }
  if (!draft.data) return <Skeleton className="h-80 w-full rounded-xl" />
  return (
    <ManifestEditor
      project={project}
      yaml={draft.data.yaml}
      draftedFrom={lockLabel(draft.data.sources)}
      onPendingChange={onPendingChange}
    />
  )
}

/**
 * ManifestEditor edits loomplane.yaml as text. A save that doesn't parse
 * comes back with every problem and where it is.
 */
export function ManifestEditor({
  project,
  yaml,
  revision,
  savedAt,
  draftedFrom,
  onPendingChange,
}: {
  project: Project
  yaml: string
  revision?: number
  savedAt?: string
  /** draftedFrom names the commits a draft was made from; set for a draft. */
  draftedFrom?: string
  onPendingChange?: (pending: boolean) => void
}) {
  const { can } = useOrganization()
  const mayEdit = can("project:update")
  const save = useMutation(useManifestsSave)
  const invalidate = useInvalidate()
  const [text, setText] = useState(yaml)
  const [lastYAML, setLastYAML] = useState(yaml)
  const [conflict, setConflict] = useState(false)
  const [problems, setProblems] = useState<string[]>([])
  // A placement save can refresh this editor while it has local changes.
  // Adopt the new YAML only when there's no draft to preserve.
  if (lastYAML !== yaml) {
    setLastYAML(yaml)
    if (text === lastYAML || text === yaml) {
      setText(yaml)
      setConflict(false)
    } else {
      setConflict(true)
    }
  }
  const dirty = text !== yaml
  useDeploymentDraft(dirty, onPendingChange)
  const drafted = draftedFrom !== undefined

  const submit = async () => {
    setProblems([])
    onPendingChange?.(true)
    try {
      const rev = await save.mutateAsync({
        path: { id: project.id },
        body: { yaml: text, baseRevision: revision ?? 0 },
      })
      invalidate(useManifestsCurrent)
      toast.success(
        rev.revision === revision
          ? "Nothing changed, so no new revision"
          : `Saved revision ${rev.revision}`
      )
    } catch (err) {
      if (statusOf(err) === 409) invalidate(useManifestsCurrent)
      setProblems(
        manifestProblems(messageOf(err, "The manifest wasn't saved."))
      )
    } finally {
      onPendingChange?.(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <FileCodeIcon className="size-4 text-muted-foreground" />
          loomplane.yaml
        </CardTitle>
        <CardDescription>
          {drafted
            ? `A draft from ${draftedFrom}: every service on the first target that runs it, and every resource found with good evidence. Check it, then save it as the first revision.`
            : "Where each service runs and what it needs. Every save adds a revision, and each plan names the one it was made from."}
        </CardDescription>
        <CardAction>
          {drafted ? (
            <Badge variant="outline" className="border-weld/60">
              Draft
            </Badge>
          ) : (
            <div className="flex items-center gap-2">
              {revision !== undefined && revision > 1 && (
                <ManifestHistory projectId={project.id} />
              )}
              <Badge variant="secondary" className="tabular-nums">
                Revision {revision}
              </Badge>
            </div>
          )}
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-3">
        {conflict && (
          <Alert>
            <AlertDescription>
              A newer manifest revision was saved. Your unsaved YAML is
              preserved. Copy your changes, then undo to load the latest
              revision before saving.
            </AlertDescription>
          </Alert>
        )}
        <Textarea
          aria-label="Manifest"
          value={text}
          onChange={(e) => setText(e.target.value)}
          readOnly={!mayEdit}
          spellCheck={false}
          aria-invalid={problems.length > 0}
          className="max-h-[32rem] min-h-64 font-mono text-xs leading-relaxed md:text-xs"
        />
        {problems.length > 0 && (
          <Alert variant="destructive">
            <AlertTitle>The manifest wasn&apos;t saved</AlertTitle>
            <AlertDescription>
              <ul className="grid gap-1">
                {problems.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        )}
      </CardContent>
      {mayEdit && (
        <CardFooter className="justify-between gap-3">
          <span className="text-xs text-muted-foreground">
            {savedAt && (
              <>
                Saved <time dateTime={savedAt}>{formatDate(savedAt)}</time>
              </>
            )}
          </span>
          <div className="flex gap-2">
            {dirty && (
              <Button
                variant="ghost"
                className="rounded-full"
                onClick={() => {
                  setText(yaml)
                  setConflict(false)
                  setProblems([])
                }}
              >
                <RotateCcwIcon />
                Undo changes
              </Button>
            )}
            <Button
              className="rounded-full"
              disabled={(!dirty && !drafted) || save.isPending || conflict}
              onClick={() => void submit()}
            >
              {save.isPending && <Spinner />}
              {drafted ? "Save manifest" : "Save revision"}
            </Button>
          </div>
        </CardFooter>
      )}
    </Card>
  )
}

/**
 * manifestProblems splits the API's "invalid manifest: a; b" into one
 * line per problem.
 */
function manifestProblems(message: string): string[] {
  const body = message.replace(/^Invalid manifest:\s*/i, "")
  if (body === message) return [message]
  return body
    .replace(/\.$/, "")
    .split("; ")
    .map((p) => p.charAt(0).toUpperCase() + p.slice(1))
}

function PlansCard({
  project,
  environments,
  environment,
  onEnvironment,
  busy,
  onOpen,
}: {
  project: Project
  environments: Environment[]
  environment: string
  onEnvironment: (value: string) => void
  busy: boolean
  onOpen: (id: string) => void
}) {
  const { can } = useOrganization()
  const plans = useQuery(usePlansList, { path: { id: project.id } })
  const list = plans.data?.plans ?? []
  useRefetchWhile(
    list.some((p) => isPlanning(p.status)),
    plans.refetch
  )

  return (
    <Card className="pb-0">
      <CardHeader>
        <CardTitle>Plans</CardTitle>
        <CardDescription>
          What deploying an environment would do, worked out from the manifest
          and one commit of each repository. Planning never touches a cloud.
        </CardDescription>
        {can("deployment:create") && environments.length > 0 && (
          <PlanControls
            project={project}
            environments={environments}
            environment={environment}
            onEnvironment={onEnvironment}
            busy={busy}
            onPlanned={(id) => {
              void plans.refetch()
              onOpen(id)
            }}
          />
        )}
      </CardHeader>
      <CardContent className="px-0">
        {plans.status === "error" ? (
          <Alert variant="destructive" className="mx-4 mb-4 w-auto">
            <AlertDescription>
              {messageOf(plans.error, "Plans couldn't be loaded.")}
            </AlertDescription>
          </Alert>
        ) : !plans.data ? (
          <div className="px-4 pb-4">
            <Skeleton className="h-24 w-full" />
          </div>
        ) : list.length === 0 ? (
          <p className="border-t px-4 py-8 text-center text-sm text-muted-foreground">
            No plans yet. Save a manifest, then plan an environment.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-4">Environment</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Changes</TableHead>
                <TableHead>From</TableHead>
                <TableHead className="pr-4">Planned</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((p) => (
                <TableRow
                  key={p.id}
                  className="cursor-pointer"
                  onClick={() => onOpen(p.id)}
                >
                  <TableCell className="pl-4 font-medium">
                    <button
                      type="button"
                      className="outline-none focus-visible:underline"
                      onClick={(e) => {
                        e.stopPropagation()
                        onOpen(p.id)
                      }}
                    >
                      {p.environment}
                    </button>
                  </TableCell>
                  <TableCell>
                    <PlanStatus status={p.status} />
                  </TableCell>
                  <TableCell className="text-muted-foreground tabular-nums">
                    {summaryLine(p)}
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {lockLabel(p.sources)} · r{p.manifestRevision}
                  </TableCell>
                  <TableCell className="pr-4 text-muted-foreground">
                    <time dateTime={p.createdAt}>
                      {formatDate(p.createdAt)}
                    </time>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

// PlanControls plans an environment, moving the repositories the scope
// picker chose. It's only shown once there's an environment, so what's
// deployed is always read for a real one.
function PlanControls({
  project,
  environments,
  environment,
  onEnvironment,
  busy,
  onPlanned,
}: {
  project: Project
  environments: Environment[]
  environment: string
  onEnvironment: (value: string) => void
  busy: boolean
  onPlanned: (id: string) => void
}) {
  const create = useMutation(usePlansCreate)
  const { aliases, known, locked, chosen, toggle, body } = useDeploymentScope(
    project.id,
    environment
  )

  const plan = async () => {
    try {
      const p = await create.mutateAsync({
        path: { id: project.id },
        body: { environment, ...body },
      })
      onPlanned(p.id)
    } catch (err) {
      toast.error(messageOf(err, "The plan couldn't start."))
    }
  }

  const title = (alias: string) => {
    if (locked.has(alias)) {
      return known
        ? `Nothing from ${alias} runs in ${environment} yet, so a plan has to include it`
        : `${alias} moves until Loomplane knows what runs in ${environment}`
    }
    return chosen.includes(alias)
      ? `${alias} moves in this plan`
      : `${alias} is held at what's deployed`
  }

  return (
    <CardAction className="flex flex-wrap items-center justify-end gap-2">
      {aliases.length > 1 &&
        aliases.map((alias) => (
          <Button
            key={alias}
            type="button"
            size="sm"
            variant={chosen.includes(alias) ? "secondary" : "outline"}
            aria-pressed={chosen.includes(alias)}
            // A locked toggle stays focusable, so its title still says why
            // it can't be turned off.
            aria-disabled={locked.has(alias) || undefined}
            className={cn(locked.has(alias) && "cursor-not-allowed")}
            title={title(alias)}
            onClick={() => toggle(alias)}
          >
            {alias}
          </Button>
        ))}
      <Select
        value={environment}
        onValueChange={(v) => onEnvironment(v ?? environment)}
      >
        <SelectTrigger size="sm" aria-label="Environment" className="min-w-32">
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
      <Button
        size="sm"
        className="rounded-full"
        disabled={
          !environment ||
          busy ||
          create.isPending ||
          (aliases.length > 0 && chosen.length === 0)
        }
        onClick={() => void plan()}
      >
        {create.isPending ? <Spinner /> : <WorkflowIcon />}
        Plan
      </Button>
    </CardAction>
  )
}

function summaryLine(p: Plan): string {
  if (isPlanning(p.status)) return "Working it out"
  if (p.status === "failed") return "None"
  if (p.problems.length > 0)
    return `${p.problems.length} ${p.problems.length === 1 ? "problem" : "problems"}`
  const n = changeCount(p)
  return n === 0
    ? "Nothing to change"
    : `${n} ${n === 1 ? "change" : "changes"}`
}

function PlanStatus({ status }: { status: string }) {
  return <StatusPill {...planStatus(status)} />
}

/** StatusPill is a status with a light that says how it's going. */
function StatusPill({ label, tone }: { label: string; tone: Tone }) {
  const s = { label, tone }
  return (
    <span className="inline-flex items-center gap-2">
      <span
        aria-hidden="true"
        className={cn(
          "inline-block h-1.5 w-3 shrink-0 rounded-full",
          s.tone === "ok" && "bg-overdye",
          s.tone === "bad" && "bg-destructive",
          s.tone === "quiet" && "bg-muted-foreground/40",
          s.tone === "wait" &&
            "animate-pulse bg-weld motion-reduce:animate-none"
        )}
      />
      {s.label}
    </span>
  )
}

function PlanReview({
  id,
  project,
  onPlanned,
}: {
  id: string
  project: Project
  onPlanned: (id: string) => void
}) {
  const plan = useQuery(usePlansGet, { path: { id } })
  useRefetchWhile(!!plan.data && isPlanning(plan.data.status), plan.refetch)
  if (plan.status === "error") {
    return (
      <div className="p-4">
        <Alert variant="destructive">
          <AlertDescription>
            {messageOf(plan.error, "The plan couldn't be loaded.")}
          </AlertDescription>
        </Alert>
      </div>
    )
  }
  if (!plan.data) {
    return (
      <div className="grid gap-3 p-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }
  return <PlanBody plan={plan.data} project={project} onPlanned={onPlanned} />
}

function PlanBody({
  plan,
  project,
  onPlanned,
}: {
  plan: Plan
  project: Project
  onPlanned: (id: string) => void
}) {
  const { can } = useOrganization()
  const create = useMutation(usePlansCreate)
  const invalidate = useInvalidate()
  const planned = !isPlanning(plan.status) && plan.status !== "failed"

  const replan = async () => {
    try {
      const p = await create.mutateAsync({
        path: { id: project.id },
        body: {
          environment: plan.environment,
          // Planning again moves what this plan moved and holds what it held.
          ...scopeBody(
            plan.sources.map((s) => s.alias),
            plan.sources.filter((s) => s.inScope).map((s) => s.alias)
          ),
        },
      })
      invalidate(usePlansList)
      onPlanned(p.id)
    } catch (err) {
      toast.error(messageOf(err, "The plan couldn't start."))
    }
  }

  return (
    <>
      <SheetHeader className="border-b">
        <SheetTitle className="flex items-center gap-3">
          Plan for {plan.environment}
          <Badge variant="outline" className="font-normal">
            <PlanStatus status={plan.status} />
          </Badge>
        </SheetTitle>
        <SheetDescription>
          Made from manifest revision {plan.manifestRevision}.{" "}
          {plan.status === "ready" && plan.expiresAt && (
            <>
              It waits for a decision until{" "}
              <time dateTime={plan.expiresAt}>
                {formatDate(plan.expiresAt)}
              </time>
              .
            </>
          )}
          <DeploysWith plan={plan} />
          {plan.decidedAt && plan.status !== "superseded" && (
            <>
              Settled{" "}
              <time dateTime={plan.decidedAt}>
                {formatDate(plan.decidedAt)}
              </time>
              {plan.decisionNote ? `: ${plan.decisionNote}` : "."}
            </>
          )}
        </SheetDescription>
      </SheetHeader>

      <div className="flex-1 divide-y overflow-y-auto">
        {plan.sources.length > 0 && (
          <Section title="Built from">
            <Lockset sources={plan.sources} />
          </Section>
        )}
        {isPlanning(plan.status) && (
          <div className="flex items-center gap-2 px-4 py-6 text-sm text-muted-foreground">
            <Spinner />
            Working out what this environment needs
          </div>
        )}
        {plan.status === "failed" && (
          <div className="px-4 py-4">
            <Alert variant="destructive">
              <AlertTitle>Planning failed</AlertTitle>
              <AlertDescription>{plan.error}</AlertDescription>
            </Alert>
          </div>
        )}
        {plan.problems.length > 0 && (
          <Section title="Problems to fix before this can be approved">
            <Findings
              findings={plan.problems}
              tone="bad"
              project={project}
              environment={plan.environment}
              sources={plan.sources}
            />
          </Section>
        )}
        {plan.warnings.length > 0 && (
          <Section title="Worth a look">
            <Findings
              findings={plan.warnings}
              tone="warn"
              project={project}
              environment={plan.environment}
              sources={plan.sources}
            />
          </Section>
        )}
        {plan.status === "approved" && plan.executable && (
          <Section title="Deployment">
            <DeploymentProgress planId={plan.id} />
          </Section>
        )}
        {planned && (
          <Section
            title={
              plan.status === "approved" ? "What it does" : "What it would do"
            }
          >
            {plan.actions.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                Nothing: what&apos;s deployed already matches the manifest.
              </p>
            ) : (
              <Steps actions={plan.actions} />
            )}
          </Section>
        )}
      </div>

      <SheetFooter className="border-t">
        {plan.status === "ready" && can("deployment:approve") ? (
          <Decide plan={plan} />
        ) : (
          !isOpen(plan.status) &&
          can("deployment:create") && (
            <Button
              variant="outline"
              className="rounded-full"
              disabled={create.isPending}
              onClick={() => void replan()}
            >
              {create.isPending ? <Spinner /> : <WorkflowIcon />}
              Plan {plan.environment} again
            </Button>
          )
        )}
      </SheetFooter>
    </>
  )
}

/**
 * Lockset lists the commit a plan pinned in each repository, and whether
 * the plan moves that repository or holds it at what's deployed.
 */
function Lockset({ sources }: { sources: PlanSource[] }) {
  return (
    <ul className="grid gap-2">
      {sources.map((s) => (
        <li
          key={s.sourceId}
          className={cn(
            "flex items-center gap-3 text-sm",
            !s.inScope && "text-muted-foreground"
          )}
        >
          <span className="min-w-16 font-medium">{s.alias}</span>
          <a
            className="font-mono text-xs underline-offset-4 hover:underline"
            href={commitURL(s.repository, s.commitSha)}
            target="_blank"
            rel="noreferrer"
          >
            {shortSHA(s.commitSha)}
          </a>
          <Badge variant={s.inScope ? "secondary" : "outline"}>
            {s.inScope ? "moves" : "held"}
          </Badge>
        </li>
      ))}
    </ul>
  )
}

function Section({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="grid grid-cols-[minmax(0,1fr)] gap-3 px-4 py-4">
      <h3 className="text-xs font-medium text-muted-foreground">{title}</h3>
      {children}
    </section>
  )
}

// A finding can rest on dozens of lines; the first few make the case.
const evidenceShown = 3

/** DeploysWith names the cloud connections a plan deploys with. */
function DeploysWith({ plan }: { plan: Plan }) {
  const list = useQuery(useConnectionsList)
  const names = planConnections(plan).map(({ cloud, id }) => {
    const c = list.data?.connections.find((x) => x.id === id)
    return `${c?.name ?? "a removed connection"} (${cloudLabel(cloud)})`
  })
  if (names.length === 0) return null
  return <>Deploys with {names.join(" and ")}. </>
}

// Findings about which cloud connection to deploy with carry the cloud
// in their path, connections.<cloud>, which isn't a manifest path.
const connectionPath = "connections."

/**
 * Findings lists a plan's problems or warnings. A resource analysis found
 * that the manifest doesn't provision can be added or ignored from here,
 * and the connection an environment deploys with chosen.
 */
function Findings({
  findings,
  tone,
  project,
  environment,
  sources,
}: {
  findings: Finding[]
  tone: "bad" | "warn"
  project: Project
  environment: string
  sources: SourceLink[]
}) {
  const { can } = useOrganization()
  const mayEdit = can("project:update")
  const mayConnect = can("cloud_account:manage")
  return (
    <ul className="grid grid-cols-[minmax(0,1fr)] gap-3">
      {findings.map((f, i) => (
        <li key={`${f.code}:${f.name ?? ""}:${i}`} className="flex gap-2.5">
          <span
            aria-hidden="true"
            className={cn(
              "mt-1.5 size-1.5 shrink-0 rounded-full",
              tone === "bad" ? "bg-destructive" : "bg-weld"
            )}
          />
          <div className="grid min-w-0 gap-1.5">
            <p className="text-sm">{f.message}</p>
            {f.path && !f.path.startsWith(connectionPath) && (
              <code className="font-mono text-xs text-muted-foreground">
                {f.path}
              </code>
            )}
            {f.code === "no-connection" && (
              <Link
                to="/integrations"
                className="text-sm underline-offset-4 hover:underline"
              >
                Connect one under Integrations
              </Link>
            )}
            {mayConnect &&
              f.path?.startsWith(connectionPath) &&
              f.code !== "no-connection" && (
                <ConnectionPicker
                  project={project}
                  environment={environment}
                  cloud={f.path.slice(connectionPath.length)}
                />
              )}
            {f.evidence && f.evidence.length > 0 && (
              <>
                <EvidenceList
                  sources={sources}
                  evidence={f.evidence.slice(0, evidenceShown)}
                />
                {f.evidence.length > evidenceShown && (
                  <p className="text-xs text-muted-foreground">
                    And {f.evidence.length - evidenceShown} more places.
                  </p>
                )}
              </>
            )}
            {mayEdit &&
              f.name &&
              (f.code === "unconfirmed-resource" ||
                f.code === "unprovided-resource") && (
                <ResourceChoice project={project} kind={f.name} />
              )}
          </div>
        </li>
      ))}
    </ul>
  )
}

/**
 * ConnectionPicker chooses which of the organization's ready connections
 * an environment deploys to a cloud with. Planning again uses it.
 */
function ConnectionPicker({
  project,
  environment,
  cloud,
}: {
  project: Project
  environment: string
  cloud: string
}) {
  const list = useQuery(useConnectionsList)
  const choose = useMutation(useEnvironmentConnectionsChoose)
  const ready =
    list.data?.connections.filter(
      (c) => c.cloud === cloud && c.status === "ready"
    ) ?? []
  if (ready.length === 0) return null

  const pick = async (id: string | null) => {
    if (!id) return
    try {
      await choose.mutateAsync({
        path: { id: project.id, environment, cloud },
        body: { connection_id: id },
      })
      const name = ready.find((c) => c.id === id)?.name ?? "it"
      toast.success(
        `${environment} deploys to ${cloudLabel(cloud)} with ${name}. Plan again to use it.`
      )
    } catch (err) {
      toast.error(messageOf(err, "The connection wasn't chosen."))
    }
  }

  return (
    <div className="pt-1">
      <Select
        onValueChange={(v) => void pick(typeof v === "string" ? v : null)}
      >
        <SelectTrigger
          size="sm"
          className="w-fit min-w-48"
          aria-label={`Connection for ${cloudLabel(cloud)}`}
          disabled={choose.isPending}
        >
          <SelectValue
            placeholder={`Choose a ${cloudLabel(cloud)} connection`}
          />
        </SelectTrigger>
        <SelectContent>
          {ready.map((c) => (
            <SelectItem key={c.id} value={c.id}>
              {c.name} · {c.account}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

/**
 * ResourceChoice answers a finding about a resource the manifest doesn't
 * provision: add one, or ignore the kind. Either saves a new manifest
 * revision; planning again shows the result.
 */
function ResourceChoice({ project, kind }: { project: Project; kind: string }) {
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const providers = useQuery(useProvidersList)
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const manifest = current.data?.manifest
  const catalog = providers.data?.providers
  if (!manifest || !catalog) return null
  // Adding a resource nothing provisions would only block the next plan.
  const provisionable = catalog.some((p) =>
    p.provisions?.some((v) => v.kind === kind)
  )

  // The edit is made to the saved YAML, on the server, so its comments
  // survive.
  const apply = async (body: EditInputBody, done: string) => {
    try {
      await save.mutateAsync({ path: { id: project.id }, body })
      invalidate(useManifestsCurrent)
      toast.success(`${done}. Plan again to see the result.`)
    } catch (err) {
      toast.error(messageOf(err, "The manifest wasn't saved."))
    }
  }

  return (
    <div className="flex flex-wrap gap-2 pt-1">
      {provisionable && (
        <Button
          size="sm"
          variant="outline"
          className="rounded-full"
          disabled={save.isPending}
          onClick={() =>
            void apply(
              { addResource: added(manifest, kind, catalog) },
              `Added ${resourceLabel(kind)} to the manifest`
            )
          }
        >
          Add {resourceLabel(kind)}
        </Button>
      )}
      <Button
        size="sm"
        variant="ghost"
        className="rounded-full"
        disabled={save.isPending}
        onClick={() =>
          void apply(
            { ignore: kind },
            `The manifest ignores ${resourceLabel(kind)} now`
          )
        }
      >
        It doesn&apos;t use {resourceLabel(kind)}
      </Button>
    </div>
  )
}

/** added is the resource withResource would add: its name, kind and provider. */
function added(m: Manifest, kind: string, catalog: Provider[]) {
  const next = withResource(m, kind, catalog)
  const name =
    Object.keys(next.resources ?? {}).find(
      (n) => !(n in (m.resources ?? {}))
    ) ?? kind
  const r = (next.resources ?? {})[name] as { provider?: string }
  return { name, kind, provider: r?.provider }
}

function Steps({ actions }: { actions: Action[] }) {
  const grouped = steps(actions)
  return (
    <ol className="grid gap-4">
      {grouped.map((group, i) => (
        <li key={group[0].step} className="grid gap-2">
          {grouped.length > 1 && (
            <p className="text-xs text-muted-foreground">
              {i === 0 ? "First" : "Then"}
            </p>
          )}
          <ul className="grid gap-2">
            {group.map((a) => (
              <ActionRow key={a.id} action={a} />
            ))}
          </ul>
        </li>
      ))}
    </ol>
  )
}

function ActionRow({ action: a }: { action: Action }) {
  const connections = useQuery(useConnectionsList)
  const target = a.connectionId
    ? connections.data?.connections.find(
        (connection) => connection.id === a.connectionId
      )
    : undefined
  const env = a.service?.env ?? []
  const routes = a.service?.gateway ?? []
  return (
    <li className="grid gap-1.5 rounded-lg border px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Badge
          variant={a.op === "delete" ? "destructive" : "secondary"}
          className="font-normal"
        >
          {opLabel(a.op)}
        </Badge>
        <span className="font-medium">{a.name}</span>
        <span className="text-sm text-muted-foreground">
          {a.creates} on {a.provider}
          {a.connectionId ? `, ${target?.name ?? a.connectionId}` : ""}
          {a.region ? `, ${a.region}` : ""}
        </span>
      </div>
      {a.reason && <p className="text-xs text-muted-foreground">{a.reason}</p>}
      {a.changes && a.changes.length > 0 && (
        <ul className="grid gap-0.5 font-mono text-xs">
          {a.changes.map((c) => (
            <li key={c.field}>
              {c.field}: <span className="text-muted-foreground">{c.from}</span>{" "}
              → {c.to}
            </li>
          ))}
        </ul>
      )}
      {routes.length > 0 && (
        <p className="text-xs text-muted-foreground">
          A gateway Loomplane builds, routing{" "}
          {routes.map((r, i) => (
            <span key={r.path}>
              {i > 0 && ", "}
              <code className="font-mono">{r.path}</code> to {r.to}
              {r.stripPrefix && " without the prefix"}
            </span>
          ))}
        </p>
      )}
      {env.length > 0 && (
        <p className="text-xs text-muted-foreground">
          Gets{" "}
          {env.map((e, i) => (
            <span key={e.key}>
              {i > 0 && ", "}
              <code className="font-mono">{e.key}</code> from {e.from}
              {e.address && (
                <>
                  {" "}
                  at <code className="font-mono">{e.address}</code>, privately
                </>
              )}
            </span>
          ))}
        </p>
      )}
    </li>
  )
}

/** Decide approves or rejects a ready plan, naming its revision. */
function Decide({ plan }: { plan: Plan }) {
  const approve = useMutation(usePlansApprove)
  const reject = useMutation(usePlansReject)
  const invalidate = useInvalidate()
  const [note, setNote] = useState("")
  const busy = approve.isPending || reject.isPending

  const decide = async (yes: boolean) => {
    try {
      const body = { revision: plan.revision, note: note.trim() || undefined }
      const p = yes
        ? await approve.mutateAsync({ path: { id: plan.id }, body })
        : await reject.mutateAsync({ path: { id: plan.id }, body })
      invalidate(usePlansList)
      invalidate(usePlansGet)
      toast.success(
        p.status === "stale"
          ? "What's deployed changed since this plan was made. Plan again."
          : yes
            ? plan.executable
              ? "Approved. Deploying now."
              : "Approved"
            : "Rejected"
      )
    } catch (err) {
      toast.error(messageOf(err, "The decision wasn't recorded."))
    }
  }

  return (
    <div className="grid w-full gap-2">
      <Textarea
        aria-label="Note"
        placeholder="A note for the record (optional)"
        value={note}
        onChange={(e) => setNote(e.target.value)}
        maxLength={2000}
        className="min-h-12"
      />
      <div className="flex justify-end gap-2">
        <Button
          variant="ghost"
          className="rounded-full"
          disabled={busy}
          onClick={() => void decide(false)}
        >
          {reject.isPending && <Spinner />}
          Reject
        </Button>
        <Button
          className="rounded-full"
          disabled={busy || plan.problems.length > 0}
          onClick={() => void decide(true)}
        >
          {approve.isPending && <Spinner />}
          Approve
        </Button>
      </div>
    </div>
  )
}

/**
 * DeploymentProgress shows an approved plan's deployment, step by step,
 * as it goes.
 */
function DeploymentProgress({ planId }: { planId: string }) {
  const deployment = useQuery(useDeploymentsForPlan, { path: { id: planId } })
  const missing =
    deployment.status === "error" && statusOf(deployment.error) === 404
  // It starts a moment after approval, then runs for a while.
  useRefetchWhile(
    missing || (!!deployment.data && isDeploying(deployment.data.status)),
    deployment.refetch,
    2000
  )
  if (missing || !deployment.data) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <Spinner />
        Starting the deployment
      </p>
    )
  }
  return <DeploymentSteps deployment={deployment.data} />
}

function DeploymentSteps({ deployment: d }: { deployment: Deployment }) {
  return (
    <div className="grid gap-3">
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <StatusPill {...deploymentStatus(d.status)} />
        <span className="text-muted-foreground">
          Started <time dateTime={d.startedAt}>{formatDate(d.startedAt)}</time>
          {d.finishedAt && (
            <>
              , finished{" "}
              <time dateTime={d.finishedAt}>{formatDate(d.finishedAt)}</time>
            </>
          )}
        </span>
      </p>
      {d.error && (
        <Alert variant="destructive">
          <AlertDescription className="wrap-anywhere">
            {d.error}
          </AlertDescription>
        </Alert>
      )}
      {d.status === "failed" && <RetryDeployment deployment={d} />}
      {d.steps.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          Nothing to change: everything was already deployed.
        </p>
      ) : (
        <ul className="grid gap-2">
          {d.steps.map((s) => (
            <li
              key={s.actionId}
              className="grid gap-1 rounded-lg border px-3 py-2.5"
            >
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span className="font-medium">{s.name}</span>
                <span className="text-sm">
                  <StatusPill {...stepStatus(s.status)} />
                </span>
                <span className="text-xs text-muted-foreground">
                  {opLabel(s.op)} on {s.provider}
                </span>
              </div>
              {s.message && (
                <p className="text-xs wrap-anywhere text-muted-foreground">
                  {s.message}
                </p>
              )}
              {s.warnings?.map((w, i) => (
                <p key={i} className="flex gap-2 text-xs wrap-anywhere">
                  <span
                    aria-hidden="true"
                    className="mt-1 size-1.5 shrink-0 rounded-full bg-weld"
                  />
                  <span>
                    <span className="sr-only">Warning: </span>
                    {w}
                  </span>
                </p>
              ))}
              {s.url && s.status === "succeeded" && (
                <a
                  href={s.url}
                  target="_blank"
                  rel="noreferrer"
                  className="w-fit text-sm underline-offset-4 hover:underline"
                >
                  {s.url.replace(/^https?:\/\//, "")}
                </a>
              )}
              {s.image && (
                <code className="font-mono text-xs wrap-anywhere text-muted-foreground">
                  {s.image}
                </code>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** DeploymentsCard lists the project's latest deployments. */
function DeploymentsCard({
  project,
  onOpen,
}: {
  project: Project
  onOpen: (planId: string) => void
}) {
  const list = useQuery(useDeploymentsList, { path: { id: project.id } })
  const deployments = list.data?.deployments ?? []
  useRefetchWhile(
    deployments.some((d) => isDeploying(d.status)),
    list.refetch,
    2000
  )
  if (list.status === "error" || deployments.length === 0) return null
  return (
    <Card className="pb-0">
      <CardHeader>
        <CardTitle>Deployments</CardTitle>
        <CardDescription>
          Each approved plan, carried out. Open one to see its steps.
        </CardDescription>
      </CardHeader>
      <CardContent className="px-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="pl-4">Environment</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Commit</TableHead>
              <TableHead className="pr-4">Started</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {deployments.map((d) => (
              <TableRow
                key={d.id}
                className="cursor-pointer"
                onClick={() => onOpen(d.planId)}
              >
                <TableCell className="pl-4 font-medium">
                  <button
                    type="button"
                    className="outline-none focus-visible:underline"
                    onClick={(e) => {
                      e.stopPropagation()
                      onOpen(d.planId)
                    }}
                  >
                    {d.environment}
                  </button>
                </TableCell>
                <TableCell>
                  <StatusPill {...deploymentStatus(d.status)} />
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {deployedCommits(d)}
                </TableCell>
                <TableCell className="pr-4 text-muted-foreground">
                  <time dateTime={d.startedAt}>{formatDate(d.startedAt)}</time>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

/**
 * RetryDeployment runs a failed deployment's failed and skipped steps
 * again, with the plan that was approved.
 */
function RetryDeployment({ deployment: d }: { deployment: Deployment }) {
  const { can } = useOrganization()
  const retry = useMutation(useDeploymentsRetry)
  const invalidate = useInvalidate()
  if (!can("deployment:approve")) return null

  const run = async () => {
    try {
      await retry.mutateAsync({ path: { id: d.id } })
      invalidate(useDeploymentsForPlan)
      invalidate(useDeploymentsList)
      toast.success("Retrying from the step that failed")
    } catch (err) {
      toast.error(messageOf(err, "The deployment couldn't be retried."))
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button
        variant="outline"
        className="rounded-full"
        disabled={retry.isPending}
        onClick={() => void run()}
      >
        {retry.isPending ? <Spinner /> : <RotateCcwIcon />}
        Retry from the failed step
      </Button>
      <span className="text-xs text-muted-foreground">
        Steps that succeeded stay as they are.
        {d.retries > 0 &&
          ` Retried ${d.retries} ${d.retries === 1 ? "time" : "times"} so far.`}
      </span>
    </div>
  )
}
