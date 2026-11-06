import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { useProjectCan } from "./project-permissions"

const workspace = vi.hoisted(() => ({ held: ["deployment:approve"] }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (permission: string) => workspace.held.includes(permission),
  }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  workspace.held = ["deployment:approve"]
})

function Probe() {
  const can = useProjectCan("proj_1")
  return (
    <ul>
      <li>update {String(can("project:update"))}</li>
      <li>approve {String(can("deployment:approve"))}</li>
    </ul>
  )
}

function view() {
  render(
    <ClientProvider client={forgeClient}>
      <Probe />
    </ClientProvider>
  )
}

test("answers for the project once loaded", async () => {
  const paths: string[] = []
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    paths.push(new URL(String(input), "http://localhost").pathname)
    return Response.json({ permissions: ["project:update"] })
  })
  view()
  // The project's answer wins over the workspace's, both ways.
  expect(await screen.findByText("update true")).toBeInTheDocument()
  expect(screen.getByText("approve false")).toBeInTheDocument()
  expect(paths).toEqual(["/api/v1/projects/proj_1/permissions"])
})

test("asks the workspace for what a project never answers", async () => {
  workspace.held = ["repository:connect", "agent:use"]
  vi.stubGlobal("fetch", async () =>
    Response.json({ permissions: ["project:update"] })
  )
  function Outside() {
    const can = useProjectCan("proj_1")
    return (
      <ul>
        <li>update {String(can("project:update"))}</li>
        <li>connect {String(can("repository:connect"))}</li>
        <li>agent {String(can("agent:use"))}</li>
      </ul>
    )
  }
  render(
    <ClientProvider client={forgeClient}>
      <Outside />
    </ClientProvider>
  )
  // The project's list only covers project permissions, so its silence on
  // the rest isn't a no.
  expect(await screen.findByText("update true")).toBeInTheDocument()
  expect(screen.getByText("connect true")).toBeInTheDocument()
  expect(screen.getByText("agent true")).toBeInTheDocument()
})

test("falls back to the workspace while loading", async () => {
  vi.stubGlobal("fetch", () => new Promise<Response>(() => undefined))
  view()
  expect(screen.getByText("update false")).toBeInTheDocument()
  expect(screen.getByText("approve true")).toBeInTheDocument()
})

test("falls back to the workspace when the answer can't be read", async () => {
  vi.stubGlobal("fetch", async () =>
    Response.json({ message: "boom" }, { status: 500 })
  )
  view()
  expect(await screen.findByText("approve true")).toBeInTheDocument()
  expect(screen.getByText("update false")).toBeInTheDocument()
})
