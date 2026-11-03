// What the agent is asking people for: a plan to approve, a secret to type,
// a question, or an account to connect. Each request shows who it was sent
// to and why, so a person who isn't the one asked can tell it isn't theirs.
import {
  CheckIcon,
  CloudIcon,
  FileCheckIcon,
  KeyRoundIcon,
  MessageCircleQuestionIcon,
  PlugIcon,
  SparklesIcon,
} from "lucide-react"
import { useId, useState, type ReactElement, type ReactNode } from "react"
import { toast } from "sonner"

import { messageOf } from "@/client/errors"
import {
  useAgentRequestsAnswer,
  useAgentRequestsComplete,
  useAgentRequestsDecline,
  useAgentRequestsList,
  useAgentRequestsProvideSecret,
  useGithubStatus,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Request } from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import { ApprovalCard } from "@/components/approval"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { buttonVariants, Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  ZeroState,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { useAgent } from "@/lib/features"
import { useOrganization } from "@/lib/organization"
import { useProjectCan } from "@/lib/project-permissions"
import { ago } from "@/lib/relative-time"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"
import { useNow, usePeople, useRuns } from "@/lib/use-runs"

type Audience = "me" | "everyone"

const kinds: Record<string, { label: string; icon: ReactNode }> = {
  approve_plan: { label: "Approve a plan", icon: <FileCheckIcon /> },
  provide_secret: { label: "Provide a secret", icon: <KeyRoundIcon /> },
  answer: { label: "A question", icon: <MessageCircleQuestionIcon /> },
  connect_github: { label: "Connect GitHub", icon: <PlugIcon /> },
  create_connection: { label: "Connect a cloud", icon: <CloudIcon /> },
}

const outcomes: Record<string, string> = {
  approved: "Approved",
  rejected: "Rejected",
  stale: "The plan went stale",
  expired: "Nobody answered in time",
  provided: "Secret provided",
  connected: "Connected",
  answered: "Answered",
  declined: "Declined",
  cancelled: "Cancelled",
}

/**
 * sentTo says who a request went to and why, in the words the person who
 * got the notice read: the team the agent named and its reason, the team
 * responsible for the project, or the workspace admins.
 */
export function sentTo(r: Request): { line: string; note: string } {
  switch (r.routeSource) {
    case "named":
      return {
        line: `Sent to ${r.teamName}`,
        note: r.teamReason || r.routeNote,
      }
    case "project":
      return {
        line: `Sent to ${r.teamName}`,
        note: ["The team responsible for this project.", r.routeNote]
          .filter(Boolean)
          .join(" "),
      }
    case "admins":
      return { line: "Sent to the workspace admins", note: r.routeNote }
    case "asker":
      return {
        line: "Asked in a conversation with the agent",
        note: r.routeNote,
      }
    default:
      return { line: "", note: "" }
  }
}

/** AgentRequests is the page of what the agent has asked for. */
export function AgentRequests({ highlight }: { highlight?: string }) {
  const { can } = useOrganization()
  const agent = useAgent()
  const [audience, setAudience] = useState<Audience>("me")
  const [settled, setSettled] = useState(!!highlight)

  return (
    <>
      <SiteHeader crumbs={[{ label: "Agent requests" }]} />
      <Page className="max-w-[880px]">
        <PageHeading
          title="Agent requests"
          description="The agent stops and asks when a change needs a person, or when it needs something only you can give it."
        />
        {!can("agent:use") ? (
          <Notice
            title="Agent requests are restricted"
            text="Ask a workspace admin for access to the agent."
          />
        ) : !agent.enabled && !agent.loading ? (
          <Notice
            title="The agent is off"
            text="Requests from the agent show up here once a workspace admin turns it on in AI settings."
          />
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div
                role="group"
                aria-label="Whose requests"
                className="inline-flex rounded-lg bg-muted p-0.5"
              >
                {(
                  [
                    ["me", "For you"],
                    ["everyone", "Everyone"],
                  ] as const
                ).map(([value, label]) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={audience === value}
                    onClick={() => setAudience(value)}
                    className={cn(
                      "h-7 rounded-md px-3 text-[13px] font-medium transition-colors",
                      audience === value
                        ? "bg-background text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground"
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <label className="flex items-center gap-2 text-[13px]">
                <Switch
                  aria-label="Show settled"
                  checked={settled}
                  onCheckedChange={setSettled}
                />
                Show settled
              </label>
            </div>
            <Requests
              audience={audience}
              settled={settled}
              highlight={highlight}
            />
          </>
        )}
      </Page>
    </>
  )
}

function Notice({ title, text }: { title: string; text: string }) {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <SparklesIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{text}</ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

function Requests({
  audience,
  settled,
  highlight,
}: {
  audience: Audience
  settled: boolean
  highlight?: string
}) {
  const list = useQuery(useAgentRequestsList, {
    query: {
      status: settled ? "all" : "pending",
      ...(audience === "me" ? { for: "me" } : {}),
    },
  })
  const projects = useQuery(useProjectsList)
  const names = new Map(
    (projects.data?.projects ?? []).map((p) => [p.id, p.name])
  )
  const all = (list.data?.requests ?? []).filter(
    (r): r is Request => r !== null
  )
  const pending = all.filter((r) => r.status === "pending")
  const rest = all.filter((r) => r.status !== "pending")
  // Who's asked what: the pending "for you" list, which the server builds
  // from the asker, team membership and the AI admins. It's the same query
  // as the default view, so reading it costs nothing there; on "everyone"
  // it tells which questions this person may answer.
  const mine = useQuery(useAgentRequestsList, {
    query: { status: "pending", for: "me" },
  })
  const forMe = new Set([
    ...(audience === "me" ? pending.map((r) => r.id) : []),
    ...(mine.data?.requests ?? []).flatMap((r) => (r ? [r.id] : [])),
  ])
  const projectIds = [...new Set(pending.map((r) => r.projectId))]

  if (list.status === "error")
    return (
      <QueryError
        error={list.error}
        fallback="The agent's requests couldn't be loaded."
        retry={list.refetch}
      />
    )
  if (!list.data) return <Skeleton className="h-40 w-full rounded-[14px]" />
  return (
    <ProjectPermissions ids={projectIds}>
      {(canOn) => (
        <div className="grid gap-3">
          {pending.length === 0 && (
            <Notice
              title="Nothing is waiting on you"
              text={
                audience === "me"
                  ? "When the agent needs you, it shows up here and in your inbox."
                  : "The agent isn't waiting on anyone right now."
              }
            />
          )}
          {pending.map((r) => (
            <RequestCard
              key={r.id}
              request={r}
              project={names.get(r.projectId)}
              highlight={r.id === highlight}
              may={maySettle(r, canOn(r.projectId), forMe)}
            />
          ))}
          {settled && rest.length > 0 && (
            <h2 className="mt-2 text-[13px] font-semibold text-muted-foreground">
              Settled
            </h2>
          )}
          {settled &&
            rest.map((r) => (
              <RequestCard
                key={r.id}
                request={r}
                project={names.get(r.projectId)}
                highlight={r.id === highlight}
                may={false}
              />
            ))}
        </div>
      )}
    </ProjectPermissions>
  )
}

type Can = (permission: string) => boolean

/**
 * ProjectPermissions reads what the person holds on each project in ids,
 * once per project however many requests it has, and hands children a
 * lookup by project. It nests one useProjectCan per project, so the hooks
 * keep their order while the list keeps its own.
 */
function ProjectPermissions({
  ids,
  found = {},
  children,
}: {
  ids: string[]
  found?: Record<string, Can>
  children: (canOn: (projectId: string) => Can) => ReactElement
}) {
  if (ids.length === 0)
    return <WorkspaceCan found={found}>{children}</WorkspaceCan>
  const [first, ...more] = ids
  return (
    <OneProject id={first} found={found} more={more}>
      {children}
    </OneProject>
  )
}

function OneProject({
  id,
  found,
  more,
  children,
}: {
  id: string
  found: Record<string, Can>
  more: string[]
  children: (canOn: (projectId: string) => Can) => ReactElement
}) {
  const can = useProjectCan(id || undefined)
  return (
    <ProjectPermissions ids={more} found={{ ...found, [id]: can }}>
      {children}
    </ProjectPermissions>
  )
}

function WorkspaceCan({
  found,
  children,
}: {
  found: Record<string, Can>
  children: (canOn: (projectId: string) => Can) => ReactElement
}) {
  const { can } = useOrganization()
  return children((projectId) => found[projectId] ?? can)
}

/**
 * maySettle is the server's rule for who settles a request, as far as the
 * page can tell: the permission its kind needs on its project, and for a
 * question, being the one asked (the "for you" list) or managing the
 * workspace's AI. The server checks again either way.
 */
function maySettle(r: Request, can: Can, forMe: Set<string>): boolean {
  switch (r.kind) {
    case "approve_plan":
      return can("deployment:approve")
    case "provide_secret":
      return can("variable:manage")
    case "connect_github":
      return can("repository:connect")
    case "create_connection":
      return can("cloud_account:manage")
    case "answer":
      return forMe.has(r.id) || can("ai:manage")
    default:
      return false
  }
}

// whoCan says who may settle a request of each kind, for someone who can
// see it but not act on it.
const whoCan: Record<string, string> = {
  approve_plan:
    "Only someone who can approve deployments in this project can settle this.",
  provide_secret:
    "Only someone who can manage this project's variables can provide it.",
  connect_github: "Only someone who can connect repositories can settle this.",
  create_connection:
    "Only someone who can manage cloud connections can settle this.",
  answer:
    "Only the person who asked, a member of the team it was sent to, or someone who manages the workspace's AI can answer.",
}

function RequestCard({
  request: r,
  project,
  highlight,
  may,
}: {
  request: Request
  project?: string
  highlight?: boolean
  may: boolean
}) {
  const now = useNow(30_000)
  const person = usePeople()
  const kind = kinds[r.kind] ?? { label: r.kind, icon: <SparklesIcon /> }
  const route = sentTo(r)
  const open = r.status === "pending"
  const id = useId()
  return (
    <Panel
      aria-labelledby={id}
      className={cn("gap-3 px-4 py-4", highlight && "ring-2 ring-primary")}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px] text-muted-foreground">
        <span
          className={cn(
            "inline-flex items-center gap-1.5 font-medium [&_svg]:size-3.5",
            open ? "text-weld-ink" : "text-foreground"
          )}
        >
          {kind.icon}
          {kind.label}
        </span>
        {project && <span>{project}</span>}
        <span className="ml-auto">Asked {ago(r.createdAt, now)}</span>
      </div>
      <h2
        id={id}
        className="text-[14px] leading-snug font-semibold text-pretty"
      >
        {r.question}
      </h2>
      {route.line && (
        <p className="text-[12.5px] leading-normal text-muted-foreground">
          <span className="font-medium text-foreground">{route.line}</span>
          {route.note && <>. {route.note.replace(/\.$/, "")}.</>}
        </p>
      )}
      {r.context.length > 0 && (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-[13px]">
          {r.context.map((l) => (
            <li key={l.path}>
              {/* The server names a dashboard path; the router prefixes the workspace. */}
              <Link
                to={l.path as "/"}
                className="text-primary underline-offset-2 hover:underline"
              >
                {l.label}
              </Link>
            </li>
          ))}
        </ul>
      )}
      {open ? (
        may ? (
          <Respond request={r} />
        ) : (
          <div className="grid gap-2">
            {r.kind === "approve_plan" && <PlanLink planId={r.target.planId} />}
            <p className="text-[13px] text-muted-foreground">
              {whoCan[r.kind] ?? "You can't settle this request."}
            </p>
          </div>
        )
      ) : (
        <p className="text-[13px] text-muted-foreground">
          <span className="font-medium text-foreground">
            {outcomes[r.outcome] ?? r.outcome}
          </span>
          {r.answeredBy && <> by {person(r.answeredBy).name}</>}
          {r.answeredAt && <>, {ago(r.answeredAt, now)}</>}
          {r.kind === "answer" && r.answer && <>: {r.answer}</>}
        </p>
      )}
    </Panel>
  )
}

// useAct runs one of a request's answers, then reads the list again.
function useAct() {
  const invalidate = useInvalidate()
  return async (run: () => Promise<unknown>, done: string) => {
    try {
      await run()
      invalidate(useAgentRequestsList)
      toast.success(done)
      return true
    } catch (err) {
      toast.error(messageOf(err, "That wasn't saved."))
      return false
    }
  }
}

function Respond({ request: r }: { request: Request }) {
  return (
    <div className="grid gap-3">
      {r.kind === "approve_plan" && <PlanApproval request={r} />}
      {r.kind === "provide_secret" && <SecretForm request={r} />}
      {r.kind === "answer" && <AnswerForm request={r} />}
      {r.kind === "connect_github" && <GitHubStep request={r} />}
      {r.kind === "create_connection" && <CloudStep request={r} />}
      <DeclineControl request={r} />
    </div>
  )
}

function PlanApproval({ request: r }: { request: Request }) {
  const { runs } = useRuns()
  const run = runs.find((x) => x.plan.id === r.target.planId)
  if (run && run.status === "waiting") return <ApprovalCard run={run} />
  return <PlanLink planId={r.target.planId} />
}

function PlanLink({ planId }: { planId?: string }) {
  return (
    <p className="text-[13px]">
      {planId ? (
        <Link
          to="/deployments/$id"
          params={{ id: planId }}
          className="text-primary underline-offset-2 hover:underline"
        >
          Open the plan
        </Link>
      ) : (
        "The plan isn't available."
      )}
    </p>
  )
}

function SecretForm({ request: r }: { request: Request }) {
  const send = useMutation(useAgentRequestsProvideSecret)
  const act = useAct()
  const [value, setValue] = useState("")
  const field = useId()
  const where = r.target.environment || "every environment"
  const submit = async () => {
    const typed = value
    if (!typed) return
    // Gone from the page the moment it's sent, whether or not it lands.
    setValue("")
    await act(
      () => send.mutateAsync({ path: { id: r.id }, body: { value: typed } }),
      `${r.target.key || "The secret"} was saved`
    )
  }
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
      className="grid gap-2"
    >
      <label htmlFor={field} className="text-[13px] font-medium">
        Value for {r.target.key} in {where}
      </label>
      <div className="flex flex-col gap-2 sm:flex-row">
        <Input
          id={field}
          type="password"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          autoComplete="new-password"
          spellCheck={false}
          className="max-w-[420px] min-w-0 flex-1 font-mono text-xs"
        />
        <Button type="submit" disabled={send.isPending || !value}>
          {send.isPending && <Spinner />}
          Save
        </Button>
      </div>
      <p className="text-[12.5px] text-muted-foreground">
        Loomplane stores it as a secret and never shows it again, to you or the
        agent. If saving fails, a second try replaces the value.
      </p>
    </form>
  )
}

function AnswerForm({ request: r }: { request: Request }) {
  const send = useMutation(useAgentRequestsAnswer)
  const act = useAct()
  const [text, setText] = useState("")
  const reply = (t: string) =>
    act(
      () => send.mutateAsync({ path: { id: r.id }, body: { text: t } }),
      "Your answer was sent"
    )
  if (r.options.length > 0)
    return (
      <div className="flex flex-wrap gap-2">
        {r.options.map((o) => (
          <Button
            key={o}
            variant="outline"
            disabled={send.isPending}
            onClick={() => void reply(o)}
          >
            {o}
          </Button>
        ))}
      </div>
    )
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (text.trim()) void reply(text.trim())
      }}
      className="grid gap-2"
    >
      <Textarea
        aria-label="Your answer"
        rows={3}
        maxLength={4000}
        value={text}
        onChange={(e) => setText(e.target.value)}
        className="bg-background"
      />
      <div>
        <Button type="submit" disabled={send.isPending || !text.trim()}>
          {send.isPending && <Spinner />}
          Send
        </Button>
      </div>
    </form>
  )
}

function GitHubStep({ request: r }: { request: Request }) {
  const github = useQuery(useGithubStatus)
  const done = useMutation(useAgentRequestsComplete)
  const act = useAct()
  const url = github.data?.installUrl
  return (
    <div className="flex flex-wrap items-center gap-2">
      {url && (
        // A link, not a button: it leaves for GitHub.
        <a href={url} className={buttonVariants({ variant: "outline" })}>
          Install the GitHub app
        </a>
      )}
      <Button
        disabled={done.isPending}
        onClick={() =>
          void act(
            () => done.mutateAsync({ path: { id: r.id } }),
            "Thanks, the agent will check"
          )
        }
      >
        {done.isPending ? <Spinner /> : <CheckIcon />}I&apos;ve connected it
      </Button>
    </div>
  )
}

function CloudStep({ request: r }: { request: Request }) {
  const done = useMutation(useAgentRequestsComplete)
  const act = useAct()
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Link to="/clouds" className={buttonVariants({ variant: "outline" })}>
        Connect {r.target.cloud || "a cloud"}
      </Link>
      <Button
        disabled={done.isPending}
        onClick={() =>
          void act(
            () => done.mutateAsync({ path: { id: r.id } }),
            "Thanks, the agent will check"
          )
        }
      >
        {done.isPending ? <Spinner /> : <CheckIcon />}Done
      </Button>
    </div>
  )
}

function DeclineControl({ request: r }: { request: Request }) {
  const decline = useMutation(useAgentRequestsDecline)
  const act = useAct()
  const [asking, setAsking] = useState(false)
  const [reason, setReason] = useState("")
  const field = useId()
  if (!asking)
    return (
      <div>
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
          onClick={() => setAsking(true)}
        >
          Decline
        </Button>
      </div>
    )
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        void act(
          () =>
            decline.mutateAsync({
              path: { id: r.id },
              body: reason.trim() ? { reason: reason.trim() } : {},
            }),
          "Declined"
        )
      }}
      className="flex flex-col gap-2 sm:flex-row sm:items-end"
    >
      <div className="grid flex-1 gap-1">
        <label htmlFor={field} className="text-[12.5px] text-muted-foreground">
          Why (optional, the agent sees it)
        </label>
        <Input
          id={field}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          maxLength={500}
        />
      </div>
      <div className="flex gap-2">
        <Button type="submit" variant="outline" disabled={decline.isPending}>
          {decline.isPending && <Spinner />}
          Decline request
        </Button>
        <Button type="button" variant="ghost" onClick={() => setAsking(false)}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
