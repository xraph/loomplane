import { useAuth } from "@authsome/ui-react"
import { Navigate, Outlet } from "@tanstack/react-router"

import { messageOf } from "@/client/errors"
import { AppSidebar } from "@/components/app-sidebar"
import { AppStatusBar } from "@/components/app-status-bar"
import { ShellProvider } from "@/components/shell"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { PageLoading } from "@/components/route-feedback"
import { OrganizationGate, useOrganization } from "@/lib/organization"
import { usePendingInvite } from "@/lib/invite"
import { usePath } from "@/lib/router"
import { AcceptInvitation } from "@/pages/accept-invitation"
import { AuthPage } from "@/pages/auth"
import { Onboarding } from "@/pages/onboarding"
import { RealtimeProvider } from "@/lib/realtime-provider"

const authPaths = ["/sign-in", "/sign-up"]
const recoveryPaths = ["/forgot-password", "/reset-password", "/verify-email"]

export function App() {
  const { isLoading, isAuthenticated } = useAuth()
  const path = usePath()

  const invite = usePendingInvite()

  if (isLoading) return <PageLoading />
  if (recoveryPaths.includes(path)) return <Outlet />
  if (!isAuthenticated) {
    return (
      <AuthPage
        mode={path === "/sign-up" ? "sign-up" : "sign-in"}
        notice={
          invite
            ? "Sign in, or create an account with the email your invitation went to, to join the organization."
            : undefined
        }
      />
    )
  }
  if (authPaths.includes(path)) return <Navigate to="/" replace />
  if (invite) return <AcceptInvitation token={invite} />
  return (
    <OrganizationGate
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
  const { session } = useOrganization()
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

export function Problem({
  title,
  message,
}: {
  title: string
  message: string
}) {
  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <Alert variant="destructive" className="max-w-md">
        <AlertTitle>{title}</AlertTitle>
        <AlertDescription>{message}</AlertDescription>
      </Alert>
    </div>
  )
}

export default App
