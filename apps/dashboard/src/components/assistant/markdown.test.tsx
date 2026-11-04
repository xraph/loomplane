import { render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { expect, it, vi } from "vitest"

import { AgentMarkdown, dashboardPath } from "./markdown"

// The router's Link, marked so a test can tell it from a plain anchor.
vi.mock("@/lib/router", () => ({
  Link: ({
    children,
    to,
    search,
    hash,
  }: {
    children?: ReactNode
    to: string
    search?: Record<string, string>
    hash?: string
  }) => (
    <a
      data-router-link=""
      href={to}
      data-search={JSON.stringify(search ?? {})}
      data-hash={hash ?? ""}
    >
      {children}
    </a>
  ),
}))

it("drops raw HTML the agent writes", () => {
  const { container } = render(
    <AgentMarkdown
      text={
        'Before <script>window.hacked = true</script> and <img src="x" onerror="window.hacked = true"> after <b>bold</b>.'
      }
    />
  )
  expect(container.querySelector("script")).toBeNull()
  expect(container.querySelector("img")).toBeNull()
  expect(container.querySelector("b")).toBeNull()
  expect(container.innerHTML).not.toContain("onerror")
  expect((window as { hacked?: boolean }).hacked).toBeUndefined()
  expect(container).toHaveTextContent("Before")
  expect(container).toHaveTextContent("after")
})

it("sends dashboard paths through the router", () => {
  render(<AgentMarkdown text="Open [the project](/projects/p1?tab=a#b)." />)
  const link = screen.getByRole("link", { name: "the project" })
  expect(link).toHaveAttribute("data-router-link")
  expect(link).toHaveAttribute("href", "/projects/p1")
  expect(link).toHaveAttribute("data-search", JSON.stringify({ tab: "a" }))
  expect(link).toHaveAttribute("data-hash", "b")
  expect(link).not.toHaveAttribute("target")
})

it("opens other sites in a new tab without a referrer", () => {
  render(<AgentMarkdown text="Read [the docs](https://e.com/docs)." />)
  const link = screen.getByRole("link", { name: "the docs" })
  expect(link).not.toHaveAttribute("data-router-link")
  expect(link).toHaveAttribute("href", "https://e.com/docs")
  expect(link).toHaveAttribute("target", "_blank")
  expect(link).toHaveAttribute("rel", "noreferrer")
})

it("doesn't make an unsafe address a link", () => {
  const { container } = render(
    <AgentMarkdown
      text={
        "Click [here](javascript:alert(1)), [there](data:text/html,hi) or [away](//evil.example) or [back](/\\evil.example)."
      }
    />
  )
  expect(screen.queryByRole("link")).toBeNull()
  expect(container.innerHTML).not.toContain("javascript:")
  expect(container).toHaveTextContent("Click here, there or away or back.")
})

it("reads only single-slash paths as dashboard pages", () => {
  expect(dashboardPath("/deployments?status=live")).toEqual({
    to: "/deployments",
    search: { status: "live" },
    hash: "",
  })
  expect(dashboardPath("//evil.example/x")).toBeUndefined()
  expect(dashboardPath("/\\evil.example")).toBeUndefined()
  expect(dashboardPath("/%5Cevil.example")).toBeUndefined()
  expect(dashboardPath("https://e.com")).toBeUndefined()
  expect(dashboardPath(undefined)).toBeUndefined()
})
