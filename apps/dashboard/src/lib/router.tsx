import { useRouterState } from "@tanstack/react-router"

export { Link, useNavigate } from "@tanstack/react-router"

/** The current pathname, subscribed to TanStack Router's history. */
export function usePath(): string {
  return useRouterState({ select: (state) => state.location.pathname })
}
