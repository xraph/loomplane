// The organization the dashboard works in, which people know as a workspace.
//
// A request acts in the authsome session's active organization, which only
// a member can switch to. The address names one (see workspace-url.ts), and
// the gate follows it: to another of the person's workspaces by switching,
// to anyone else's by reporting it missing. A fresh sign-in at an address
// without one switches to the workspace this browser used last (remembered
// as a preference, never as a credential) or the first, and asks for a new
// workspace when there are none.
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react"

import { statusOf } from "@/client/errors"
import {
  useListOrganizations,
  useSwitchOrg,
} from "@/client/forge/authsome/hooks"
import type { Organization } from "@/client/forge/authsome/types"
import { useSessionGet } from "@/client/forge/loomplane/hooks"
import type { IdentitySession } from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { forgeClient, setTabOrganization } from "@/client/runtime"
import { setWorkspaceSlug } from "@/lib/workspace-url"

const lastOrgKey = "loomplane.last-org"

type Value = {
  session: IdentitySession
  organizations: Organization[]
  /** The workspace's slug, which every signed-in address starts with. */
  slug: string
  /** A slug the address names that isn't one of the person's workspaces. */
  missing: string | null
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
 * organization, and fallback(state) until then. workspace is the slug the
 * address names, if any. onSwitch runs after a switch the person asked for,
 * once links point into the new workspace and before it renders.
 */
export function OrganizationGate({
  children,
  fallback,
  workspace = null,
  onSwitch,
}: {
  children: ReactNode
  fallback: (state: GateState) => ReactNode
  workspace?: string | null
  onSwitch?: () => Promise<void> | void
}) {
  const session = useQuery(useSessionGet)
  const orgs = useQuery(useListOrganizations)
  const switchOrg = useMutation(useSwitchOrg)
  const { refetch: refetchSession } = session
  const { refetch: refetchOrgs } = orgs
  const [transitioning, setTransitioning] = useState(false)
  const [synchronized, setSynchronized] = useState("")
  const switching = switchOrg.isPending || transitioning

  const activate = useCallback(
    async (orgId: string, asked = false) => {
      setTransitioning(true)
      try {
        await switchOrg.mutateAsync({ body: { orgId } })
        window.localStorage.setItem(lastOrgKey, orgId)
        // The session is in the new organization now. Without this the
        // refetch still names the old one, and the API refuses it as a tab
        // that's fallen out of step (409).
        setTabOrganization(orgId)
        await refetchSession()
        // A workspace created a moment ago isn't in the list yet.
        const listing = await refetchOrgs()
        if (asked) {
          // Before the gate opens again: the address still names the old
          // workspace, which would switch straight back.
          setWorkspaceSlug(slugOf(listing.organizations, orgId))
          await onSwitch?.()
        }
      } finally {
        setTransitioning(false)
      }
    },
    [switchOrg, refetchSession, refetchOrgs, onSwitch]
  )

  const active = session.data
  const organizations = useMemo(
    () => orgs.data?.organizations ?? [],
    [orgs.data]
  )
  const listed = orgs.status === "success"
  const slug = active && listed ? slugOf(organizations, active.orgId) : ""
  const principal = active ? `${active.userId}:${active.orgId}` : ""
  const synced = `${principal}:${slug}`
  useEffect(() => {
    if (!active || !slug || switching || session.status !== "success") return
    // Children stay unmounted until the header and cache owner agree. Cache
    // subscribers may refetch synchronously when the principal changes.
    setTabOrganization(active.orgId)
    setWorkspaceSlug(slug)
    forgeClient.setPrincipal(principal)
    // This state acknowledges the external header/cache transition before admission.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setSynchronized(synced)
  }, [active, slug, principal, synced, switching, session.status])

  const needsOrg =
    !active && session.status === "error" && statusOf(session.error) === 403
  const named = workspace
    ? organizations.find((o) => o.slug === workspace)
    : undefined
  const remembered = window.localStorage.getItem(lastOrgKey)
  const pick =
    named ?? organizations.find((o) => o.id === remembered) ?? organizations[0]
  // The address names another of the person's workspaces.
  const follow =
    active && named && named.id !== active.orgId ? named.id : undefined
  const missing = listed && workspace && !named ? workspace : null

  useEffect(() => {
    if (follow && !switching && switchOrg.status !== "error") {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void activate(follow)
    }
  }, [follow, switching, switchOrg.status, activate])

  useEffect(() => {
    if (needsOrg && pick && !switching && switchOrg.status !== "error") {
      // Automatic activation enters the same committed gate as a user switch.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void activate(pick.id)
    }
  }, [needsOrg, pick, switching, switchOrg.status, activate])

  const value = useMemo<Value | null>(
    () =>
      active
        ? {
            session: active,
            organizations,
            slug,
            missing,
            switchOrganization: (orgId) => activate(orgId, true),
            can: (p) => active.permissions.includes(p),
          }
        : null,
    [active, organizations, slug, missing, activate]
  )

  if (
    value &&
    session.status === "success" &&
    !switching &&
    !follow &&
    synchronized === synced
  )
    return <Ctx.Provider value={value}>{children}</Ctx.Provider>
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

// An organization from before slugs were required goes by its ID.
function slugOf(organizations: Organization[], orgId: string): string {
  return organizations.find((o) => o.id === orgId)?.slug || orgId
}

export function useOrganization(): Value {
  const v = useContext(Ctx)
  if (!v) throw new Error("useOrganization outside OrganizationGate")
  return v
}
