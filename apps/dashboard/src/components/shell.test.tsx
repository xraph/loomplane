import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ShellProvider, useShell } from "@/components/shell"
import { ThemeProvider } from "@/components/theme-provider"

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  usePath: () => "/",
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
afterEach(() => {
  vi.unstubAllGlobals()
  navigate.mockReset()
  forgeClient.clear()
})
function Entry() {
  const { openDeploy } = useShell()
  return (
    <button
      onClick={() => openDeploy({ project: "p", environment: "preview" })}
    >
      Deploy this project
    </button>
  )
}
it("opens the full deployment configuration with the project and environment preset", async () => {
  vi.stubGlobal(
    "fetch",
    async () =>
      new Response(JSON.stringify({ projects: [], installations: [] }), {
        headers: { "Content-Type": "application/json" },
      })
  )
  render(
    <ClientProvider client={forgeClient}>
      <ThemeProvider>
        <ShellProvider>
          <Entry />
        </ShellProvider>
      </ThemeProvider>
    </ClientProvider>
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Deploy this project" })
  )
  expect(navigate).toHaveBeenCalledWith({
    to: "/deployments/new",
    search: { project: "p", environment: "preview" },
  })
})
