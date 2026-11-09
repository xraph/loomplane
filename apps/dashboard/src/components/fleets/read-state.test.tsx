import { fireEvent, render, screen } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { FleetReadState } from "./read-state"
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
it("keeps loading separate from an empty result", () => {
  render(<FleetReadState projectID="project" loading retry={async () => {}} />)
  expect(screen.getByRole("status")).toHaveTextContent("Loading fleet state")
  expect(screen.queryByText(/No .*yet/)).not.toBeInTheDocument()
})
it.each([
  [403, "Fleet access denied"],
  [404, "Fleet resource unavailable"],
  [500, "Could not load fleet state"],
])(
  "renders status %s without disclosing internal errors",
  async (status, title) => {
    const retry = vi.fn(async () => {})
    render(
      <FleetReadState
        projectID="project"
        error={{ statusCode: status, message: "private-token" }}
        retry={retry}
      />
    )
    expect(screen.getByText(title)).toBeInTheDocument()
    expect(screen.queryByText(/private-token/)).not.toBeInTheDocument()
    if (status === 500) {
      fireEvent.click(screen.getByRole("button", { name: "Retry" }))
      expect(retry).toHaveBeenCalledOnce()
    }
    expect(
      screen.getByText(title).closest('[data-slot="empty"]')
    ).not.toBeNull()
  }
)
