import { createRouter, type RouterHistory } from "@tanstack/react-router"

import {
  PageError,
  PageLoading,
  PageNotFound,
} from "@/components/route-feedback"
import { routeTree } from "./routeTree.gen"

export function createDashboardRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    history,
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
