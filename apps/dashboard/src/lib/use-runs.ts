// The hooks the deployment views load runs with. Plans and deployments
// are listed per project, so the organization-wide views ask every project
// and join the answers here. An organization-wide endpoint would make this
// one request; until there is one, the cache shares each project's lists
// with the project page, so nothing is fetched twice.
import type {
  QueryBinding,
  QueryState,
  TagContext,
} from "@forge-go/client-core"
import { useCallback, useEffect, useMemo, useState } from "react"

import {
  useBindingsList,
  useDeploymentsForPlan,
  useDeploymentsList,
  useEnvironmentsList,
  useMembersList,
  usePlansList,
  useProjectsGet,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  Binding,
  Deployment,
  DeploymentList,
  OrgMember,
  Plan,
  PlanList,
  Project,
} from "@/client/forge/loomplane/types"
import { useQuery, useRefetchWhile } from "@/client/hooks"
import { useQueries } from "@/client/queries"
import {
  bridgeFrom,
  horizon,
  type Joined,
  joinPages,
  needsOlder,
  type Page,
} from "@/lib/pages"
import { isLive, type Run, toRun } from "@/lib/runs"

// Often enough that a running step visibly moves, rarely enough that a
// dozen projects don't flood the API.
const liveInterval = 2000

export interface Runs {
  runs: Run[]
  projects: Project[]
  /** loaded is true once every project's plans and deployments arrived. */
  loaded: boolean
  error?: unknown
  refetch: () => Promise<unknown>
  /**
   * whole is how far back runs is complete: an older run may be missing
   * one from another project that hasn't been read that far yet. It's
   * undefined once every project is read to its first run.
   */
  whole?: string
  /** more reads an older page of every project that has one. */
  more: () => void
  /** loadingMore is true while older pages are on their way. */
  loadingMore: boolean
}

type ListKind = "plans" | "deployments"

// Module-level, so the memos that take them see the same function each time.
const planPage = (l: PlanList): Page<Plan> => ({ items: l.plans, next: l.next })
const deploymentPage = (l: DeploymentList): Page<Deployment> => ({
  items: l.deployments,
  next: l.next,
})
type Older = Record<string, Record<ListKind, string[]>>

/**
 * useRuns is every run in the organization, newest first. It starts with
 * each project's newest page and reads older ones on more(). However far
 * back it has read, each loaded plan's deployment is read too.
 */
export function useRuns(): Runs {
  const projects = useQuery(useProjectsList)
  const list = useMemo(() => projects.data?.projects ?? [], [projects.data])
  const args = useMemo(() => list.map((p) => ({ path: { id: p.id } })), [list])
  const plans = useQueries(usePlansList, args)
  const deployments = useQueries(useDeploymentsList, args)

  // The cursors each project's older pages were read from, oldest last.
  const [older, setOlder] = useState<Older>({})
  const olderPlans = useOlderPages(
    usePlansList,
    list,
    plans.states,
    older,
    "plans",
    planPage
  )
  const olderDeployments = useOlderPages(
    useDeploymentsList,
    list,
    deployments.states,
    older,
    "deployments",
    deploymentPage
  )

  const runs = useMemo(() => {
    const byProject = new Map(list.map((p) => [p.id, p]))
    const byPlan = new Map<string, Deployment>()
    for (const j of olderDeployments.joined.values()) {
      for (const d of j.items) {
        // Newest first from the API, so the first one per plan is the latest.
        if (!byPlan.has(d.planId)) byPlan.set(d.planId, d)
      }
    }
    const out: Run[] = []
    for (const j of olderPlans.joined.values()) {
      for (const p of j.items) {
        out.push(toRun(p, byPlan.get(p.id), byProject.get(p.projectId)))
      }
    }
    return out.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  }, [list, olderPlans.joined, olderDeployments.joined])

  const read = useCallback(
    (project: string, kind: ListKind, cursor: string) =>
      setOlder((o) => {
        const had = o[project] ?? { plans: [], deployments: [] }
        if (had[kind].includes(cursor)) return o
        return { ...o, [project]: { ...had, [kind]: [...had[kind], cursor] } }
      }),
    []
  )

  // A project's deployments are read back until each loaded plan that was
  // approved has its own. What's missing follows from what
  // has loaded, so it's settled here as it renders rather than an effect
  // later: React renders again at once, before anything is shown.
  const behind = list.flatMap((p) => {
    const ds = olderDeployments.joined.get(p.id)
    const next = ds?.next
    return next &&
      needsOlder(ds, olderPlans.joined.get(p.id)?.items ?? []) &&
      !older[p.id]?.deployments.includes(next)
      ? [{ project: p.id, next }]
      : []
  })
  if (behind.length > 0) {
    for (const b of behind) read(b.project, "deployments", b.next)
  }

  const whole = useMemo(
    () =>
      horizon(
        list.map((p) => {
          const j = olderPlans.joined.get(p.id)
          return j && (j.next || j.loading)
            ? j.items.at(-1)?.createdAt
            : undefined
        })
      ),
    [list, olderPlans.joined]
  )
  const more = useCallback(() => {
    for (const p of list) {
      const next = olderPlans.joined.get(p.id)?.next
      if (next) read(p.id, "plans", next)
    }
  }, [list, olderPlans.joined, read])

  const moving = runs.some(isLive)
  useRefetchWhile(moving, plans.refetch, liveInterval)
  useRefetchWhile(moving, deployments.refetch, liveInterval)

  const failed = [
    projects,
    ...plans.states,
    ...deployments.states,
    ...olderPlans.states,
    ...olderDeployments.states,
  ].find((s) => s.status === "error")
  const refetchProjects = projects.refetch
  const refetchPlans = plans.refetch
  const refetchDeployments = deployments.refetch
  const refetchOlderPlans = olderPlans.refetch
  const refetchOlderDeployments = olderDeployments.refetch
  const refetch = useCallback(
    () =>
      Promise.allSettled([
        refetchProjects(),
        refetchPlans(),
        refetchDeployments(),
        refetchOlderPlans(),
        refetchOlderDeployments(),
      ]),
    [
      refetchProjects,
      refetchPlans,
      refetchDeployments,
      refetchOlderPlans,
      refetchOlderDeployments,
    ]
  )
  const pending = (s: { data?: unknown; status: string }) =>
    !s.data && s.status !== "error"
  return {
    runs,
    projects: list,
    loaded:
      (!!projects.data || projects.status === "error") &&
      !plans.states.some(pending) &&
      !deployments.states.some(pending),
    error: failed?.error,
    refetch,
    whole,
    more,
    loadingMore: [...olderPlans.states, ...olderDeployments.states].some(
      pending
    ),
  }
}

/**
 * useOlderPages reads the older pages of one list in every project, from
 * the cursors in older, and joins each project's pages, first page
 * included. When the first page has moved on since the older ones were
 * read, it also reads the rows that fell between them.
 */
function useOlderPages<L, T extends { id: string }>(
  op: QueryBinding<L>,
  projects: Project[],
  first: QueryState<L>[],
  older: Older,
  kind: ListKind,
  pick: (l: L) => Page<T>
): {
  joined: Map<string, Joined<T>>
  states: QueryState<L>[]
  refetch: () => Promise<unknown>
} {
  // Which project each older page belongs to, beside its arguments, so
  // its answer can be put back with the project's first page.
  const plan = useMemo(() => {
    const owners: string[] = []
    const args: TagContext[] = []
    projects.forEach((p, i) => {
      const cursors = older[p.id]?.[kind] ?? []
      const firstNext = first[i]?.data ? pick(first[i].data).next : undefined
      const bridge = bridgeFrom(firstNext, cursors)
      // The bridge reads as many as the API allows, so it reaches back to
      // where the older pages begin unless a lot has happened since.
      if (bridge) {
        owners.push(p.id)
        args.push({ path: { id: p.id }, query: { before: bridge, limit: 100 } })
      }
      for (const before of cursors) {
        owners.push(p.id)
        args.push({ path: { id: p.id }, query: { before } })
      }
    })
    return { owners, args }
  }, [projects, first, older, kind, pick])
  const pages = useQueries(op, plan.args)

  const joined = useMemo(() => {
    const out = new Map<string, Joined<T>>()
    projects.forEach((p, i) => {
      const own: (Page<T> | undefined)[] = [
        first[i]?.data ? pick(first[i].data) : undefined,
      ]
      plan.owners.forEach((owner, j) => {
        if (owner !== p.id) return
        const s = pages.states[j]
        own.push(
          s?.data
            ? pick(s.data)
            : s?.status === "error"
              ? { items: [] }
              : undefined
        )
      })
      out.set(p.id, joinPages(own))
    })
    return out
  }, [projects, first, plan.owners, pages.states, pick])
  return { joined, states: pages.states, refetch: pages.refetch }
}

/**
 * useRunFor follows one plan's run: its project and its deployment, watched
 * while it moves. The page loads the plan first and calls this once it has
 * one, so nothing is asked for with an empty ID.
 */
export function useRunFor(
  plan: Plan,
  refetchPlan: () => Promise<unknown>
): Run {
  const project = useQuery(useProjectsGet, { path: { id: plan.projectId } })
  // Only an approved plan has a deployment; asking sooner is a 404 each time.
  const approved = plan.status === "approved"
  const forPlan = useMemo(
    () => (approved ? [{ path: { id: plan.id } }] : []),
    [approved, plan.id]
  )
  const deployment = useQueries(useDeploymentsForPlan, forPlan)
  const d = deployment.states[0]?.data
  const run = useMemo(
    () => toRun(plan, d, project.data),
    [plan, d, project.data]
  )
  // An approved plan's deployment appears a moment after approval and then
  // runs; a plan being worked out or waiting can change under the page too.
  const watching = isLive(run) || (plan.status === "approved" && !d)
  useRefetchWhile(watching, refetchPlan, liveInterval)
  useRefetchWhile(watching, deployment.refetch, liveInterval)
  return run
}

/** Person is how a member is shown: a name and two initials. */
export interface Person {
  name: string
  initials: string
}

/** usePeople names the organization's members by user ID. */
export function usePeople(): (userID: string) => Person {
  const members = useQuery(useMembersList)
  const byID = useMemo(
    () =>
      new Map(
        (members.data?.members ?? []).map((m: OrgMember) => [m.userId, m])
      ),
    [members.data]
  )
  // Stable while the members don't change, so views can memoize on it.
  return useCallback(
    (id: string) => {
      const m = byID.get(id)
      const name = m?.name || m?.email || "Someone"
      return { name, initials: initialsOf(name) }
    },
    [byID]
  )
}

/** initialsOf is two letters for an avatar. */
export function initialsOf(name: string): string {
  return (
    name
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join("") || "?"
  )
}

/** useNow is the time, ticking every interval, for durations that grow. */
export function useNow(interval = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), interval)
    return () => window.clearInterval(id)
  }, [interval])
  return now
}

export interface Running {
  bindings: Binding[]
  /** Environment names by ID, for labelling a binding. */
  envNames: Map<string, string>
  loaded: boolean
  error?: unknown
  refetch: () => Promise<unknown>
}

/**
 * useRunning is everything deployed across the organization: each
 * project's bindings in each of its environments. The API lists bindings
 * per environment, so this asks every one; an organization-wide list would
 * make it a single request.
 */
export function useRunning(): Running {
  const projects = useQuery(useProjectsList)
  const list = useMemo(() => projects.data?.projects ?? [], [projects.data])
  const byProject = useMemo(
    () => list.map((p) => ({ path: { id: p.id } })),
    [list]
  )
  const environments = useQueries(useEnvironmentsList, byProject)
  const envs = useMemo(
    () => environments.states.flatMap((s) => s.data?.environments ?? []),
    [environments.states]
  )
  const byEnv = useMemo(
    () =>
      envs.map((e) => ({
        path: { id: e.projectId },
        query: { environment: e.name },
      })),
    [envs]
  )
  const bindings = useQueries(useBindingsList, byEnv)
  const all = useMemo(
    () => bindings.states.flatMap((s) => s.data?.bindings ?? []),
    [bindings.states]
  )
  const envNames = useMemo(
    () => new Map(envs.map((e) => [e.id, e.name])),
    [envs]
  )
  const failed = [projects, ...environments.states, ...bindings.states].find(
    (s) => s.status === "error"
  )
  const refetchProjects = projects.refetch
  const refetchEnvironments = environments.refetch
  const refetchBindings = bindings.refetch
  const refetch = useCallback(
    () =>
      Promise.allSettled([
        refetchProjects(),
        refetchEnvironments(),
        refetchBindings(),
      ]),
    [refetchProjects, refetchEnvironments, refetchBindings]
  )
  return {
    bindings: all,
    envNames,
    loaded:
      (!!projects.data || projects.status === "error") &&
      environments.states.every((s) => !!s.data || s.status === "error") &&
      bindings.states.every((s) => !!s.data || s.status === "error"),
    error: failed?.error,
    refetch,
  }
}
