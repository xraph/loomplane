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
import { AuthLayout } from "@/components/auth-layout"
import { ShellProvider } from "@/components/shell"
import { SystemAddress, SystemPage } from "@/components/system-page"
import { Button } from "@/components/ui/button"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { PageLoading, WorkspaceNotFound } from "@/components/route-feedback"
import { OrganizationGate, useOrganization } from "@/lib/organization"
import { useInstance } from "@/lib/instance"
import { usePendingInvite } from "@/lib/invite"
import { usePath } from "@/lib/router"
import { AcceptInvitation } from "@/pages/accept-invitation"
import { AuthPage } from "@/pages/auth"
import { Onboarding } from "@/pages/onboarding"
import { SetupPage } from "@/pages/setup"
import { RealtimeProvider } from "@/lib/realtime-provider"
import { isUnprefixed, requestedWorkspace } from "@/lib/workspace-url"

const authPaths = ["/sign-in", "/sign-up"]
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
  const { isLoading, isAuthenticated } = useAuth()
  const path = usePath()

  const invite = usePendingInvite()
  const { setupPending } = useInstance()

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
  if (recoveryPaths.includes(path)) {
    return (
      <AuthLayout title={authTitle(path)}>
        <Outlet />
      </AuthLayout>
    )
  }
  if (!isAuthenticated) {
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
  if (authPaths.includes(path)) return <Navigate to="/" replace />
  if (invite) return <AcceptInvitation token={invite} />
  // An invitation link without its token: the route says so on a page of
  // its own, since no workspace is involved yet.
  if (path === "/invitations/accept") return <Outlet />
  return <SignedIn />
}

function SignedIn() {
  const router = useRouter()
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
            return <Onboarding activate={state.activate} />
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
  useWorkspaceAddress()
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
      <SidebarProvider>
        <AppSidebar />
        {/* Keyed on the organization, so switching remounts every page. */}
        <SidebarInset key={session.orgId} className="min-w-0">
          <Outlet />
          <AppStatusBar />
        </SidebarInset>
      </SidebarProvider>
    </ShellProvider>
  )
}

// An address without a workspace, from a bookmark or an old link, moves
// under the current one. The router reads it again once the workspace is
// known, in case the slug is also a page's name, such as /projects/projects.
function useWorkspaceAddress() {
  const router = useRouter()
  const location = useRouterState({ select: (state) => state.location })
  const { slug } = useOrganization()
  const named = requestedWorkspace(location)
  const stale =
    named === null && !isUnprefixed(location.pathname) ? location.href : ""
  useEffect(() => {
    if (stale) void router.navigate({ href: stale, replace: true })
  }, [router, stale, slug])
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
