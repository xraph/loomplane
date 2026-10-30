import { useRouter, useRouterState } from "@tanstack/react-router"
import { useEffect, useRef } from "react"

import { PageLoading } from "@/components/route-feedback"
import { setWorkspaceSlug } from "@/lib/workspace-url"

const destinationKey = "loomplane.auth.destination"

function localDestination(value: string | null): string {
  if (!value?.startsWith("/") || value.startsWith("//")) return "/"
  const url = new URL(value, window.location.origin)
  if (url.origin !== window.location.origin) return "/"
  if (
    /^\/(sign-in|sign-up|forgot-password|reset-password|verify-email)\/?$/.test(
      url.pathname
    )
  )
    return "/"
  return `${url.pathname}${url.search}${url.hash}`
}

/** Keep the requested address in this tab while you sign in or recover your account. */
export function AuthRedirect({
  authenticated = false,
}: {
  authenticated?: boolean
}) {
  const router = useRouter()
  const href = useRouterState({ select: (state) => state.location.publicHref })
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true
    let destination = "/"
    try {
      if (authenticated) {
        destination = localDestination(sessionStorage.getItem(destinationKey))
        sessionStorage.removeItem(destinationKey)
      } else {
        sessionStorage.setItem(destinationKey, localDestination(href))
      }
    } catch {
      // You can still sign in when the browser disables session storage.
    }
    // A previous session's workspace must not rewrite the saved address.
    setWorkspaceSlug("")
    if (authenticated) {
      // This is already a public address, including its workspace prefix.
      router.history.replace(destination)
    } else {
      void router.navigate({ to: "/sign-in", replace: true })
    }
  }, [authenticated, href, router])

  return <PageLoading />
}
