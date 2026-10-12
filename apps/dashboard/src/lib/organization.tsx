// The organization the dashboard works in.
//
// A request acts in the authsome session's active organization, which only
// a member can switch to. A fresh sign-in has none, so the gate switches to
// the organization this browser used last (remembered as a preference,
// never as a credential) or the first one, and asks for a new organization
// when there are none.
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
} from "react"

import { statusOf } from "@/client/errors"
import {
  useListOrganizations,
  useSwitchOrg,
} from "@/client/forge/authsome/hooks"
import type { Organization } from "@/client/forge/authsome/types"
import { useSessionGet } from "@/client/forge/loomplane/hooks"
import type { Session } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { forgeClient, setTabOrganization } from "@/client/runtime"

const lastOrgKey = "loomplane.last-org"

type Value = {
  session: Session
  organizations: Organization[]
  switchOrganization: (orgId: string) => Promise<void>
  /** can reports whether the session holds a permission, such as "project:create". */
  can: (permission: string) => boolean
}

const Ctx = createContext<Value | null>(null)

export type GateState =
  | { kind: "loading" }
  | { kind: "needs-organization"; activate: (orgId: string) => Promise<void> }
  | { kind: "error"; error: unknown }

/**
 * OrganizationGate renders children once the session acts in an
 * organization, and fallback(state) until then.
 */
export function OrganizationGate({
  children,
  fallback,
}: {
  children: ReactNode
  fallback: (state: GateState) => ReactNode
}) {
  const session = useQuery(useSessionGet)
  const orgs = useQuery(useListOrganizations)
  const switchOrg = useMutation(useSwitchOrg)
  const { refetch: refetchSession } = session
  const switching = switchOrg.isPending

  const activate = useCallback(
    async (orgId: string) => {
      await switchOrg.mutateAsync({ body: { orgId } })
      window.localStorage.setItem(lastOrgKey, orgId)
      await refetchSession()
    },
    [switchOrg, refetchSession]
  )

  const active = session.data
  if (active) {
    // On every render, not in an effect: a child's effect runs before its
    // parent's, so a child could otherwise read the previous organization's
    // rows. setPrincipal empties the cache only when the value changes.
    setTabOrganization(active.orgId)
    forgeClient.setPrincipal(`${active.userId}:${active.orgId}`)
  }

  const needsOrg =
    !active && session.status === "error" && statusOf(session.error) === 403
  const organizations = useMemo(
    () => orgs.data?.organizations ?? [],
    [orgs.data]
  )
  const remembered = window.localStorage.getItem(lastOrgKey)
  const pick =
    organizations.find((o) => o.id === remembered) ?? organizations[0]

  useEffect(() => {
    if (needsOrg && pick && !switching && switchOrg.status !== "error") {
      void activate(pick.id)
    }
  }, [needsOrg, pick, switching, switchOrg.status, activate])

  const value = useMemo<Value | null>(
    () =>
      active
        ? {
            session: active,
            organizations,
            switchOrganization: activate,
            can: (p) => active.permissions.includes(p),
          }
        : null,
    [active, organizations, activate]
  )

  if (value) return <Ctx.Provider value={value}>{children}</Ctx.Provider>
  if (session.status === "error" && !needsOrg) {
    return fallback({ kind: "error", error: session.error })
  }
  if (switchOrg.status === "error") {
    return fallback({ kind: "error", error: switchOrg.error })
  }
  if (needsOrg && orgs.status === "success" && organizations.length === 0) {
    return fallback({ kind: "needs-organization", activate })
  }
  return fallback({ kind: "loading" })
}

export function useOrganization(): Value {
  const v = useContext(Ctx)
  if (!v) throw new Error("useOrganization outside OrganizationGate")
  return v
}
