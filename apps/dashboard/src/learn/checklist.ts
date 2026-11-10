// The getting-started list. Most tasks tick themselves from what the
// workspace already has: a project, a cloud account, a deploy that went
// out, a second member. The ones Loomplane can't see are ticked by hand.
import {
  useConnectionsList,
  useGithubStatus,
  useMembersList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { useRuns } from "@/lib/use-runs"

/** The pages a task's button can open. */
export type TaskRoute =
  | "/integrations"
  | "/projects"
  | "/clouds"
  | "/deployments/new"
  | "/members"
  | "/notifications/settings"

export type Task = {
  id: string
  title: string
  body: string
  /** About how long it takes, shown while it's open. */
  minutes: number
  done: boolean
  /** Ticked by hand rather than read from the workspace. */
  manual: boolean
  action: { label: string; to: TaskRoute }
  /** A guided lesson that walks through it. */
  lesson?: string
}

/** What the workspace has, as far as the list can tell. Unknown counts
 * (still loading, or not yours to see) are undefined. */
export type WorkspaceFacts = {
  projects?: number
  clouds?: number
  codeHosts?: number
  succeeded?: number
  members?: number
}

/** checklistTasks lays the list out from what's known and what was ticked. */
export function checklistTasks(f: WorkspaceFacts, ticked: string[]): Task[] {
  const known = (n: number | undefined, id: string) =>
    n === undefined ? ticked.includes(id) : n > 0
  const tasks: Omit<Task, "done" | "manual">[] = [
    {
      id: "code",
      title: "Connect your code",
      body: "Loomplane reads the repositories you pick on GitHub, GitLab or Bitbucket. Nothing else in your account is touched.",
      minutes: 2,
      action: { label: "Open integrations", to: "/integrations" },
    },
    {
      id: "project",
      title: "Create a project",
      body: "A project points at a repository. Loomplane reads its services and suggests where each one should run.",
      minutes: 3,
      action: { label: "Open projects", to: "/projects" },
      lesson: "first-project",
    },
    {
      id: "cloud",
      title: "Connect a cloud account",
      body: "Loomplane deploys into accounts you own. You sign in with the provider, and it asks only for the access it needs.",
      minutes: 2,
      action: { label: "Connect a cloud", to: "/clouds" },
    },
    {
      id: "deploy",
      title: "Ship your first deploy",
      body: "Pick a project and an environment, read the plan Loomplane writes, then approve it and watch it roll out.",
      minutes: 5,
      action: { label: "New deployment", to: "/deployments/new" },
      lesson: "first-deploy",
    },
    {
      id: "team",
      title: "Invite your team",
      body: "Teammates can review plans, approve deploys and get alerts. You choose who can approve production.",
      minutes: 1,
      action: { label: "Open members", to: "/members" },
      lesson: "invite-teammate",
    },
    {
      id: "alerts",
      title: "Choose what reaches you",
      body: "Pick which failures and approvals notify you, here and on your phone.",
      minutes: 2,
      action: { label: "Notification settings", to: "/notifications/settings" },
    },
  ]
  // A project means its code is connected, whichever host it came from.
  const code =
    f.codeHosts === undefined && f.projects === undefined
      ? undefined
      : (f.codeHosts ?? 0) + (f.projects ?? 0)
  const facts: Record<string, number | undefined> = {
    code,
    project: f.projects,
    cloud: f.clouds,
    deploy: f.succeeded,
    team: f.members === undefined ? undefined : f.members - 1,
  }
  return tasks.map((t) => {
    const manual = !(t.id in facts) || facts[t.id] === undefined
    return { ...t, manual, done: known(facts[t.id], t.id) }
  })
}

/** useChecklist is the list for this workspace, with what's still loading
 * counted as not known yet. */
export function useChecklist(ticked: string[]): {
  tasks: Task[]
  loaded: boolean
} {
  const projects = useQuery(useProjectsList)
  const connections = useQuery(useConnectionsList)
  const github = useQuery(useGithubStatus)
  const members = useQuery(useMembersList)
  const { runs, loaded: runsLoaded } = useRuns()
  // An empty list can arrive as null, so a loaded response counts as zero.
  const count = (loaded: unknown, list?: unknown[] | null) =>
    loaded ? (list?.length ?? 0) : undefined
  const facts: WorkspaceFacts = {
    projects: count(projects.data, projects.data?.projects),
    clouds: count(connections.data, connections.data?.connections),
    codeHosts: count(github.data, github.data?.installations),
    succeeded: runsLoaded
      ? runs.filter((r) => r.status === "succeeded").length
      : undefined,
    members: count(members.data, members.data?.members),
  }
  return {
    tasks: checklistTasks(facts, ticked),
    loaded: !!projects.data && !!connections.data && runsLoaded,
  }
}
