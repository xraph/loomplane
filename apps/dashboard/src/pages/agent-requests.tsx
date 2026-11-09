// What the agent is asking people for: a plan to approve, a secret to type,
// a question, or an account to connect. Each request shows who it was sent
// to and why, so a person who isn't the one asked can tell it isn't theirs.
import { SparklesIcon } from "lucide-react"
import { useState, type ReactElement } from "react"

import {
  useAgentRequestsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Request } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import {
  AgentRequestCard,
  type Can,
  maySettle,
} from "@/components/agent-request-card"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
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
import { cn } from "@/lib/utils"

export { sentTo } from "@/components/agent-request-card"

type Audience = "me" | "everyone"

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
            <AgentRequestCard
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
              <AgentRequestCard
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
