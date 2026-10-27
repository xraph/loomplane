import { createRouter, type RouterHistory } from "@tanstack/react-router"

import {
  PageError,
  PageLoading,
  PageNotFound,
} from "@/components/route-feedback"
import { workspaceRewrite } from "@/lib/workspace-url"
import { routeTree } from "./routeTree.gen"

// The first segment of every top-level route: an address that starts with
// one of these is a page, not a workspace.
const pages = new Set(
  Object.values(routeTree.children ?? {}).map(
    (route) => route.options.path.split("/").filter(Boolean)[0] ?? ""
  )
)

export function createDashboardRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    history,
    rewrite: workspaceRewrite(pages),
    scrollRestoration: true,
    defaultPendingComponent: PageLoading,
    defaultErrorComponent: PageError,
    defaultNotFoundComponent: PageNotFound,
  })
}

export const router = createDashboardRouter()

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}
