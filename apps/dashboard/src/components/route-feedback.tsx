import { Link } from "@tanstack/react-router"

import { Button } from "@/components/ui/button"
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
      <Button render={<Link to="/" />}>Back to projects</Button>
    </div>
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
