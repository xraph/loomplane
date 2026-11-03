// What the server has switched on, from its public config. The dashboard
// hides a feature's pages and links when it's off, and a route for one
// sends you home.
import { redirect } from "@tanstack/react-router"
import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
} from "react"

import {
  useAgentRequestsList,
  useAgentStatusGet,
} from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { useOrganization } from "@/lib/organization"

export type Features = { ai: boolean }

const off: Features = { ai: false }

const FeaturesContext = createContext<Features>(off)

// Routes check features before anything renders, outside React, so the
// dashboard records them here once at startup, from the same public config
// the provider gets.
const current: { features: Features } = { features: off }

/**
 * setFeatures records what the server has on, for requireFeature. main.tsx
 * calls it once, before the router loads; tests call it to switch AI on.
 */
export function setFeatures(features: Features | undefined): void {
  current.features = { ...off, ...features }
}

export function FeaturesProvider({
  features,
  children,
}: {
  features: Features | undefined
  children: ReactNode
}) {
  const value = useMemo(() => ({ ...off, ...features }), [features])
  return (
    <FeaturesContext.Provider value={value}>
      {children}
    </FeaturesContext.Provider>
  )
}

/** useFeatures says what the server has on. Everything is off by default. */
export function useFeatures(): Features {
  return useContext(FeaturesContext)
}

/**
 * requireFeature is for a route's beforeLoad: it sends you to the overview
 * when the server has the feature off.
 */
export function requireFeature(feature: keyof Features): void {
  if (!current.features[feature]) throw redirect({ to: "/", replace: true })
}

/** What the workspace's agent is doing for this person. */
export type Agent = {
  /** The agent is on for this workspace. Always false when the server has AI off. */
  enabled: boolean
  /** The agent can search the workspace's documents. */
  knowledge: boolean
  /** The status hasn't arrived yet. */
  loading: boolean
  /** Requests waiting on this person's answer, for the sidebar's badge. */
  pending: number
}

const noAgent: Agent = {
  enabled: false,
  knowledge: false,
  loading: false,
  pending: 0,
}

const AgentContext = createContext<Agent>(noAgent)

// The agent's requests are polled this often. The realtime stream carries
// their changes, but the generated client doesn't tag the requests' reads
// as the entity it invalidates, so the count catches up on a timer too.
const requestsInterval = 20_000

/**
 * AgentProvider reads whether the agent is on in this workspace, once, for
 * the sidebar and the pages that mention it. With the server's AI off it
 * renders nothing of its own and asks the API nothing, because the agent's
 * routes don't exist then.
 */
export function AgentProvider({ children }: { children: ReactNode }) {
  const { ai } = useFeatures()
  if (!ai) return <>{children}</>
  return <AgentStatus>{children}</AgentStatus>
}

function AgentStatus({ children }: { children: ReactNode }) {
  const { can } = useOrganization()
  const status = useQuery(useAgentStatusGet)
  const enabled = !!status.data?.enabled
  const base: Agent = {
    enabled,
    knowledge: enabled && !!status.data?.knowledge,
    loading: status.status === "pending" || status.status === "idle",
    pending: 0,
  }
  if (enabled && can("agent:use"))
    return <AgentPending base={base}>{children}</AgentPending>
  return <AgentContext.Provider value={base}>{children}</AgentContext.Provider>
}

function AgentPending({
  base,
  children,
}: {
  base: Agent
  children: ReactNode
}) {
  const list = useQuery(useAgentRequestsList, { query: { for: "me" } })
  useEffect(() => {
    const id = window.setInterval(() => {
      // A failed refetch leaves the last count; the page shows the error.
      list.refetch().catch(() => undefined)
    }, requestsInterval)
    return () => window.clearInterval(id)
  }, [list])
  const pending = (list.data?.requests ?? []).filter(
    (r) => r?.status === "pending"
  ).length
  const value = useMemo(() => ({ ...base, pending }), [base, pending])
  return <AgentContext.Provider value={value}>{children}</AgentContext.Provider>
}

/**
 * useAgent says whether the agent is on for this workspace. It is all off
 * with the server's AI off, and nothing is asked of the API then.
 */
export function useAgent(): Agent {
  return useContext(AgentContext)
}
