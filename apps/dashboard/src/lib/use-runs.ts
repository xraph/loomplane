// The hooks the deployment views load runs with. Plans and deployments
// are listed per project, so the organization-wide views ask every project
// and join the answers here. An organization-wide endpoint would make this
// one request; until there is one, the cache shares each project's lists
// with the project page, so nothing is fetched twice.
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
  OrgMember,
  Plan,
  Project,
} from "@/client/forge/loomplane/types"
import { useQuery, useRefetchWhile } from "@/client/hooks"
import { useQueries } from "@/client/queries"
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
}

/** useRuns is every run in the organization, newest first. */
export function useRuns(): Runs {
  const projects = useQuery(useProjectsList)
  const list = useMemo(() => projects.data?.projects ?? [], [projects.data])
  const args = useMemo(() => list.map((p) => ({ path: { id: p.id } })), [list])
  const plans = useQueries(usePlansList, args)
  const deployments = useQueries(useDeploymentsList, args)

  const runs = useMemo(() => {
    const byProject = new Map(list.map((p) => [p.id, p]))
    const byPlan = new Map<string, Deployment>()
    for (const s of deployments.states) {
      for (const d of s.data?.deployments ?? []) {
        // Newest first from the API, so the first one per plan is the latest.
        if (!byPlan.has(d.planId)) byPlan.set(d.planId, d)
      }
    }
    const out: Run[] = []
    for (const s of plans.states) {
      for (const p of s.data?.plans ?? []) {
        out.push(toRun(p, byPlan.get(p.id), byProject.get(p.projectId)))
      }
    }
    return out.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  }, [list, plans.states, deployments.states])

  const moving = runs.some(isLive)
  useRefetchWhile(moving, plans.refetch, liveInterval)
  useRefetchWhile(moving, deployments.refetch, liveInterval)

  const failed = [projects, ...plans.states, ...deployments.states].find(
    (s) => s.status === "error"
  )
  const refetchProjects = projects.refetch
  const refetchPlans = plans.refetch
  const refetchDeployments = deployments.refetch
  const refetch = useCallback(
    () =>
      Promise.allSettled([
        refetchProjects(),
        refetchPlans(),
        refetchDeployments(),
      ]),
    [refetchProjects, refetchPlans, refetchDeployments]
  )
  return {
    runs,
    projects: list,
    loaded:
      (!!projects.data || projects.status === "error") &&
      plans.states.every((s) => !!s.data || s.status === "error") &&
      deployments.states.every((s) => !!s.data || s.status === "error"),
    error: failed?.error,
    refetch,
  }
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
