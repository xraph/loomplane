import { useAuth } from "@authsome/ui-react"
import {
  Navigate,
  Outlet,
  useRouter,
  useRouterState,
} from "@tanstack/react-router"
import { useCallback, useEffect } from "react"

import { messageOf } from "@/client/errors"
import { AppSidebar } from "@/components/app-sidebar"
import { AppStatusBar } from "@/components/app-status-bar"
import { AuthRedirect, FreshLoad } from "@/components/auth-redirect"
import { AuthLayout } from "@/components/auth-layout"
import { ShellProvider } from "@/components/shell"
import { SystemAddress, SystemPage } from "@/components/system-page"
import { Button } from "@/components/ui/button"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { PageLoading, WorkspaceNotFound } from "@/components/route-feedback"
import { AgentProvider } from "@/lib/features"
import { OrganizationGate, useOrganization } from "@/lib/organization"
import { useInstance } from "@/lib/instance"
import { usePendingInvite } from "@/lib/invite"
import { usePath } from "@/lib/router"
import { AuthPage } from "@/pages/auth"
import { CloudSignInStep } from "@/pages/cloud-sign-in-step"
import { Onboarding } from "@/pages/onboarding"
import { SetupPage } from "@/pages/setup"
import { RealtimeProvider } from "@/lib/realtime-provider"
import { isUnprefixed, requestedWorkspace } from "@/lib/workspace-url"

const stepPath = "/setup/cloud-sign-in"
const authPaths = ["/sign-in", "/sign-up"]
// Full-load callbacks that carry a code or state in their query. The step
// must not take them over; it shows on the next ordinary navigation.
const callbackPaths = [
  "/integrations/oauth/callback",
  "/integrations/github/setup",
  "/device",
]
const recoveryPaths = ["/forgot-password", "/reset-password", "/verify-email"]

// Every signed-out screen shares one AuthLayout, returned from the same place
// below, so moving between them swaps the card and keeps the frame mounted.
function authTitle(path: string) {
  switch (path) {
    case "/setup":
      return "Set up Loomplane"
    case "/sign-up":
      return "Create your Loomplane account"
    case "/forgot-password":
    case "/reset-password":
      return "Recover your Loomplane account"
    case "/verify-email":
      return "Verify your email"
    default:
      return "Sign in to Loomplane"
  }
}

export function App() {
  const { isLoading, isAuthenticated, user } = useAuth()
  const path = usePath()

  const invite = usePendingInvite()
  const {
    setupPending,
    requireEmailVerification,
    platformAdmin,
    cloudSignInOffer,
  } = useInstance()

  if (isLoading) return <PageLoading />
  // A fresh instance has nobody to sign in as: every address leads to setup,
  // in the one frame, so the card swaps without the layout remounting.
  if (setupPending && !isAuthenticated) {
    return (
      <AuthLayout title={authTitle("/setup")}>
        {path !== "/setup" && <Navigate to="/setup" replace />}
        <SetupPage />
      </AuthLayout>
    )
  }
  // Setup is done, or you're signed in: there's nothing to set up.
  if (!setupPending && path === "/setup") {
    return <Navigate to="/sign-in" replace />
  }
  // A signed-in account whose email is already verified has no code to
  // enter, so the verify page would only ask for one that doesn't exist.
  // A full load: the public config was read before this account verified,
  // so it can't yet say whether to offer cloud sign-in.
  if (path === "/verify-email" && isAuthenticated && user?.email_verified) {
    return <FreshLoad to="/" />
  }
  if (recoveryPaths.includes(path)) {
    return (
      <AuthLayout title={authTitle(path)}>
        <Outlet />
      </AuthLayout>
    )
  }
  // Sign-up leaves a session behind before the email is verified, so a
  // second tab arrives signed in. The API refuses that session; send it to
  // verify its email instead of into pages that would all fail.
  if (isAuthenticated && requireEmailVerification && !user?.email_verified) {
    return (
      <Navigate
        to="/verify-email"
        search={{ email: user?.email ?? "" }}
        replace
      />
    )
  }
  if (!isAuthenticated) {
    if (!authPaths.includes(path)) return <AuthRedirect />
    return (
      <AuthLayout title={authTitle(path)}>
        <AuthPage
          mode={path === "/sign-up" ? "sign-up" : "sign-in"}
          notice={
            invite
              ? "Sign in, or create an account with the email your invitation went to, to join the workspace."
              : undefined
          }
        />
      </AuthLayout>
    )
  }
  // A new instance's first platform admin is offered cloud sign-in apps once,
  // on a page of its own, before anything else.
  if (
    platformAdmin &&
    cloudSignInOffer &&
    path !== stepPath &&
    !callbackPaths.includes(path)
  ) {
    return <Navigate to={stepPath} replace />
  }
  if (path === stepPath) {
    if (!platformAdmin) return <Navigate to="/" replace />
    return (
      <AuthLayout title="Set up cloud sign-in">
        <CloudSignInStep />
      </AuthLayout>
    )
  }
  if (authPaths.includes(path)) return <AuthRedirect authenticated />
  // The invitation has a page of its own, outside any workspace. Wherever
  // signing in or verifying an email left you, a pending one goes there.
  if (path === "/invitations/accept") return <Outlet />
  // So has approving a loomctl sign-in, which isn't tied to a workspace.
  if (path === "/device") return <Outlet />
  if (invite) return <Navigate to="/invitations/accept" replace />
  return <SignedIn />
}

function SignedIn() {
  const router = useRouter()
  const path = usePath()
  const workspace = useRouterState({
    select: (state) => requestedWorkspace(state.location),
  })
  // Another workspace's pages are another workspace's records: start over
  // on its home page rather than at the same path.
  const home = useCallback(() => router.navigate({ to: "/" }), [router])
  return (
    <OrganizationGate
      workspace={workspace}
      onSwitch={home}
      fallback={(state) => {
        switch (state.kind) {
          case "loading":
            return <PageLoading />
          case "needs-organization":
            // Its own address, as setup has, rather than whichever page
            // you asked for, which there's no workspace to show.
            return (
              <>
                {path !== "/onboarding" && (
                  <Navigate to="/onboarding" replace />
                )}
                <Onboarding activate={state.activate} />
              </>
            )
          case "error":
            return (
              <Problem
                title="Loomplane couldn't load your session"
                message={messageOf(state.error, "Reload to try again.")}
              />
            )
        }
      }}
    >
      <RealtimeProvider>
        <Shell />
      </RealtimeProvider>
    </OrganizationGate>
  )
}

function Shell() {
  const { session, organizations, missing } = useOrganization()
  const path = usePath()
  const addressPending = useWorkspaceAddress()
  if (addressPending) return <PageLoading />
  // A new workspace starts in the onboarding frame, not inside this one.
  if (path === "/workspaces/new") return <Outlet />
  if (missing) {
    const current = organizations.find((o) => o.id === session.orgId)
    return (
      <WorkspaceNotFound
        slug={missing}
        path={path}
        current={current?.name ?? "your workspace"}
      />
    )
  }
  return (
    <ShellProvider>
      {/* Keyed on the organization: the agent is on or off per workspace. */}
      <AgentProvider key={session.orgId}>
        <SidebarProvider>
          <AppSidebar />
          {/* Keyed on the organization, so switching remounts every page. */}
          <SidebarInset key={session.orgId} className="min-w-0">
            <Outlet />
            <AppStatusBar />
          </SidebarInset>
        </SidebarProvider>
      </AgentProvider>
    </ShellProvider>
  )
}

// An address without a workspace, from a bookmark or an old link, moves
// under the current one. The router reads it again once the workspace is
// known, in case the slug is also a page's name, such as /projects/projects.
function useWorkspaceAddress() {
  const router = useRouter()
  const location = useRouterState({ select: (state) => state.location })
  const routing = useRouterState({
    select: (state) => state.status === "pending",
  })
  const { slug } = useOrganization()
  const named = requestedWorkspace(location)
  const stale =
    named === null && !isUnprefixed(location.pathname) ? location.href : ""
  useEffect(() => {
    if (stale) void router.navigate({ href: stale, replace: true })
  }, [router, stale, slug])
  return !!stale || routing
}

export function Problem({
  title,
  message,
  address,
  retry = true,
}: {
  title: string
  message: string
  /** The address that failed, when there is one worth showing. */
  address?: string
  /** Whether trying again could help. A broken link can't be retried. */
  retry?: boolean
}) {
  return (
    <SystemPage
      tone="problem"
      title={title}
      lede={message}
      actions={
        retry && (
          <Button onClick={() => window.location.reload()}>Try again</Button>
        )
      }
    >
      {address && <SystemAddress before={address} />}
    </SystemPage>
  )
}

export default App
