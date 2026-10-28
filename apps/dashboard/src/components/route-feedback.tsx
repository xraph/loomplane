import { Link } from "@tanstack/react-router"

import { SystemAddress, SystemPage } from "@/components/system-page"
import { Button, buttonVariants } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"

export function PageLoading() {
  return (
    <div
      className="flex min-h-svh flex-1 items-center justify-center"
      role="status"
      aria-label="Loading page"
    >
      <Spinner className="size-6 text-muted-foreground" />
    </div>
  )
}

export function PageNotFound() {
  return (
    <div className="flex min-h-svh flex-1 flex-col items-start justify-center gap-4 p-6 md:p-10">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">
          Page not found
        </h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Check the address, or go back to your projects.
        </p>
      </div>
      <Link to="/projects" className={buttonVariants()}>
        Back to projects
      </Link>
    </div>
  )
}

/**
 * WorkspaceNotFound answers an address in a workspace the person isn't in.
 * It's a full page, outside the shell: the sidebar belongs to a workspace,
 * and this address doesn't open one.
 */
export function WorkspaceNotFound({
  slug,
  path,
  current,
}: {
  slug: string
  /** The rest of the address, after the slug. */
  path: string
  /** The name of the workspace the person is signed in to. */
  current: string
}) {
  return (
    <SystemPage
      tone="problem"
      title="Workspace not found"
      lede="You're not a member of a workspace at this address. Ask its owner for an invitation, or go back to your own."
      actions={
        <Link to="/" className={buttonVariants()}>
          Go to {current}
        </Link>
      }
    >
      <SystemAddress
        before={`${window.location.host}/`}
        marked={slug}
        after={path === "/" ? "" : path}
      />
    </SystemPage>
  )
}

export function PageError() {
  return (
    <div
      className="flex min-h-svh flex-1 flex-col items-start justify-center gap-4 p-6 md:p-10"
      role="alert"
    >
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">
          This page couldn't load
        </h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Reload to try again. If it keeps failing, check your connection.
        </p>
      </div>
      <Button onClick={() => window.location.reload()}>Reload page</Button>
    </div>
  )
}
