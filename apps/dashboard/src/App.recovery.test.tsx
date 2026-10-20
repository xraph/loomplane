import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import App from "@/App"

const auth = vi.hoisted(() => ({ isAuthenticated: false, isLoading: false }))
const route = vi.hoisted(() => ({ path: "/forgot-password" }))

vi.mock("@authsome/ui-react", () => ({ useAuth: () => auth }))
vi.mock("@/lib/router", async (original) => ({
  ...(await original<typeof import("@/lib/router")>()),
  usePath: () => route.path,
}))
vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Outlet: () => <main>Public recovery route</main>,
}))

// Removing the guard exception would replace recovery with a sign-in form,
// making emailed reset links unusable for the people who need them most.
describe("public recovery routing", () => {
  for (const path of ["/forgot-password", "/reset-password", "/verify-email"]) {
    for (const isAuthenticated of [false, true]) {
      it(`opens ${path} when authenticated=${isAuthenticated}`, () => {
        route.path = path
        auth.isAuthenticated = isAuthenticated
        render(<App />)
        expect(screen.getByRole("main")).toHaveTextContent(
          "Public recovery route"
        )
      })
    }
  }
})
