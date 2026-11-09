import { useRouterState } from "@tanstack/react-router"

export { Link, useBlocker, useNavigate } from "@tanstack/react-router"

/** The current pathname, subscribed to TanStack Router's history. */
export function usePath(): string {
  return useRouterState({ select: (state) => state.location.pathname })
}

/** The current search, as the router parsed it. */
export function useSearchParams(): Record<string, unknown> {
  return useRouterState({
    select: (state) => state.location.search as Record<string, unknown>,
  })
}
