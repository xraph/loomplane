// The mock's only state: deployments, plus a clock. Everything on screen is
// derived from those two, so a deployment advances through its steps, its
// logs stream and the overview updates without any timers of their own.
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react"
import { toast } from "sonner"

import {
  APPROVAL_STEP,
  type Deployment,
  type Env,
  initialDeployments,
  me,
  type Project,
  projects as seedProjects,
  project as findProject,
  services,
  type Step,
  steps,
} from "@/mock/data"
import {
  type ProjectColorName,
  type ProjectIconName,
  randomColor,
  randomIcon,
} from "@/lib/project-style"
import { navigate } from "@/mock/router"

export type StepState =
  "done" | "running" | "waiting" | "failed" | "canceled" | "pending" | "skipped"

export type DeploymentStatus =
  "running" | "waiting" | "succeeded" | "failed" | "canceled"

export type StepView = {
  index: number
  step: Step
  state: StepState
  startedAt?: number
  durationMs?: number
  progress?: number
}

export type DeploymentView = Deployment & {
  status: DeploymentStatus
  steps: StepView[]
  current: number
  finishedAt?: number
  elapsedMs: number
}

/** view works out where a deployment is at a moment in time. */
export function view(d: Deployment, now: number): DeploymentView {
  const out: StepView[] = []
  let t = d.startedAt
  let status: DeploymentStatus = "succeeded"
  let current = steps.length - 1
  let finishedAt: number | undefined
  let stopped = false

  for (let i = 0; i < steps.length; i++) {
    const step = steps[i] as Step
    if (stopped) {
      out.push({
        index: i,
        step,
        state:
          status === "running" || status === "waiting" ? "pending" : "skipped",
      })
      continue
    }
    let dur = (d.durations[i] ?? 1) * 1000
    if (i === APPROVAL_STEP && d.requiresApproval) {
      if (d.approvedAt === undefined) {
        if (d.canceledAt !== undefined) {
          out.push({ index: i, step, state: "canceled", startedAt: t })
          status = "canceled"
          finishedAt = d.canceledAt
        } else {
          out.push({ index: i, step, state: "waiting", startedAt: t })
          status = "waiting"
        }
        current = i
        stopped = true
        continue
      }
      dur = Math.max(d.approvedAt - t, 1000)
    }
    const end = t + dur
    if (d.canceledAt !== undefined && d.canceledAt < end) {
      out.push({
        index: i,
        step,
        state: "canceled",
        startedAt: t,
        durationMs: Math.max(d.canceledAt - t, 0),
      })
      status = "canceled"
      finishedAt = d.canceledAt
      current = i
      stopped = true
      continue
    }
    if (now < end) {
      out.push({
        index: i,
        step,
        state: "running",
        startedAt: t,
        progress: Math.min(Math.max((now - t) / dur, 0), 1),
      })
      status = "running"
      current = i
      stopped = true
      continue
    }
    if (d.failedAt === i) {
      out.push({
        index: i,
        step,
        state: "failed",
        startedAt: t,
        durationMs: dur,
      })
      status = "failed"
      finishedAt = end
      current = i
      stopped = true
      continue
    }
    out.push({ index: i, step, state: "done", startedAt: t, durationMs: dur })
    t = end
  }
  if (!stopped) finishedAt = t
  return {
    ...d,
    status,
    steps: out,
    current,
    finishedAt,
    elapsedMs: (finishedAt ?? now) - d.startedAt,
  }
}

export function isLive(d: { status: DeploymentStatus }) {
  return d.status === "running" || d.status === "waiting"
}

type Action =
  | { type: "approve"; id: string; at: number }
  | { type: "cancel"; id: string; at: number }
  | { type: "add"; deployment: Deployment }

function reducer(state: Deployment[], action: Action): Deployment[] {
  switch (action.type) {
    case "approve":
      return state.map((d) =>
        d.id === action.id
          ? { ...d, approvedAt: action.at, approvedBy: me.id }
          : d
      )
    case "cancel":
      return state.map((d) =>
        d.id === action.id ? { ...d, canceledAt: action.at } : d
      )
    case "add":
      return [action.deployment, ...state]
  }
}

function randomHex(n: number) {
  let s = ""
  for (let i = 0; i < n; i++) s += Math.floor(Math.random() * 16).toString(16)
  return s
}

export type DeployRequest = { project: string; env: Env; ref: string }

export type NewProject = {
  repo: string
  name: string
  description: string
  icon: ProjectIconName | null
  color: ProjectColorName | null
}

type Store = {
  now: number
  deployments: DeploymentView[]
  projects: Project[]
  createProject: (input: NewProject) => Project
  restyleProject: (
    id: string,
    style: { icon?: ProjectIconName; color?: ProjectColorName }
  ) => void
  approve: (id: string) => void
  cancel: (id: string) => void
  deploy: (req: DeployRequest) => string
  redeploy: (id: string) => string
}

const StoreContext = createContext<Store | null>(null)

const label: Record<Env, string> = {
  production: "production",
  staging: "staging",
  preview: "preview",
}

export function MockStoreProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(reducer, initialDeployments)
  const [projects, setProjects] = useState<Project[]>(seedProjects)
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [])

  const deployments = useMemo(
    () =>
      state.map((d) => view(d, now)).sort((a, b) => b.startedAt - a.startedAt),
    [state, now]
  )

  // Announce the moments people wait for: a release going live, a failure,
  // a plan that needs someone.
  const seen = useRef(new Map<string, DeploymentStatus>())
  useEffect(() => {
    const first = seen.current.size === 0
    for (const d of deployments) {
      const before = seen.current.get(d.id)
      seen.current.set(d.id, d.status)
      if (first || before === undefined || before === d.status) continue
      const where = `${d.project} on ${label[d.env]}`
      const open = {
        label: "View",
        onClick: () => navigate(`/deployments/${d.id}`),
      }
      if (d.status === "succeeded") {
        toast.success(`Deployed ${where}`, {
          description: d.message,
          action: open,
        })
      } else if (d.status === "failed") {
        toast.error(`Deploy failed for ${where}`, { action: open })
      } else if (d.status === "waiting") {
        toast(`Plan for ${where} needs approval`, { action: open })
      }
    }
  }, [deployments])

  const approve = useCallback((id: string) => {
    dispatch({ type: "approve", id, at: Date.now() })
  }, [])

  const cancel = useCallback((id: string) => {
    dispatch({ type: "cancel", id, at: Date.now() })
  }, [])

  const deploy = useCallback(({ project, env, ref }: DeployRequest) => {
    const id = `dpl_${randomHex(6)}`
    const sha = randomHex(7)
    const proj = findProject(project)
    // Fast enough to watch from start to finish.
    const durations = steps.map((s) =>
      Math.max(0.5, s.typical * 0.08 * (0.8 + Math.random() * 0.4))
    )
    dispatch({
      type: "add",
      deployment: {
        id,
        project,
        env,
        branch: ref || proj?.branch || "main",
        sha,
        message: `Deploy ${ref || "main"} from the dashboard`,
        author: me.id,
        trigger: "manual",
        startedAt: Date.now(),
        durations,
        // Same policy as the seeded data: production always waits for an admin.
        requiresApproval: env === "production",
        changes: services
          .filter((s) => s.project === project)
          .map((s) => ({
            action: "update" as const,
            kind: s.runtime,
            name: s.name,
            provider: s.provider,
            attrs: [{ key: "build", after: sha }],
            cost: 0,
          })),
        dispatchRun: `run_01JA${randomHex(6).toUpperCase()}`,
        ctrlPlaneRelease: `rel_${randomHex(8)}`,
      },
    })
    return id
  }, [])

  const redeploy = useCallback(
    (id: string) => {
      const d = state.find((x) => x.id === id)
      if (!d) return id
      return deploy({ project: d.project, env: d.env, ref: d.branch })
    },
    [state, deploy]
  )

  const createProject = useCallback((input: NewProject) => {
    const slug = input.name
      .toLowerCase()
      .replace(/[^a-z0-9-]+/g, "-")
      .replace(/^-+|-+$/g, "")
    const created: Project = {
      id: slug,
      name: slug,
      description: input.description,
      repo: input.repo,
      branch: "main",
      envs: [
        { env: "production", url: `${slug}.acme.dev` },
        { env: "preview", url: `preview.${slug}.acme.dev` },
      ],
      manifestRevision: 1,
      // Nothing picked means a random choice, so every project still has one.
      icon: input.icon ?? randomIcon(),
      color: input.color ?? randomColor(),
    }
    setProjects((ps) => [...ps, created])
    return created
  }, [])

  const restyleProject = useCallback(
    (
      id: string,
      style: { icon?: ProjectIconName; color?: ProjectColorName }
    ) => {
      setProjects((ps) => ps.map((p) => (p.id === id ? { ...p, ...style } : p)))
    },
    []
  )

  const value = useMemo(
    () => ({
      now,
      deployments,
      projects,
      createProject,
      restyleProject,
      approve,
      cancel,
      deploy,
      redeploy,
    }),
    [
      now,
      deployments,
      projects,
      createProject,
      restyleProject,
      approve,
      cancel,
      deploy,
      redeploy,
    ]
  )

  return <StoreContext.Provider value={value}>{children}</StoreContext.Provider>
}

export function useStore(): Store {
  const s = useContext(StoreContext)
  if (!s) throw new Error("useStore outside MockStoreProvider")
  return s
}
