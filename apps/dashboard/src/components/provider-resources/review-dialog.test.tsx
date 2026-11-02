import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ResourceReviewDialog } from "./review-dialog"

vi.mock("@/lib/router", () => ({ useNavigate: () => vi.fn() }))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
test("creation options come from the installed registry and checked accounts", async () => {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path.endsWith("projects"))
      return Response.json({ projects: [{ id: "p", name: "Orders" }] })
    if (path.endsWith("environments"))
      return Response.json({ environments: [{ name: "production" }] })
    if (path.endsWith("manifest"))
      return Response.json({
        manifest: { version: 1, services: { api: { source: "." } } },
      })
    throw new Error(path)
  })
  render(
    <ClientProvider client={forgeClient}>
      <ResourceReviewDialog
        open
        onOpenChange={() => {}}
        providers={[
          {
            name: "future",
            title: "Future Provider",
            cloud: "future",
            available: true,
            provisions: [{ kind: "future-db", creates: "Future database" }],
            regions: ["west"],
          } as never,
        ]}
        connections={[
          {
            id: "c",
            name: "Checked future account",
            cloud: "future",
            status: "ready",
          } as never,
        ]}
      />
    </ClientProvider>
  )
  expect(await screen.findByText("Future database")).toBeInTheDocument()
  expect(screen.getByRole("combobox", { name: "Account" })).toHaveTextContent(
    "Checked future account"
  )
  await userEvent.click(screen.getByRole("combobox", { name: "Resource kind" }))
  expect(
    await screen.findByRole("option", { name: "future-db" })
  ).toBeInTheDocument()
})

test("required provider settings gate review and are saved in the manifest", async () => {
  const writes: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        writes.push({ path, body: JSON.parse(String(init.body)) })
        return Response.json(
          path.endsWith("edits") ? { revision: 2 } : { id: "plan-1" }
        )
      }
      if (path.endsWith("projects"))
        return Response.json({ projects: [{ id: "p", name: "Orders" }] })
      if (path.endsWith("environments"))
        return Response.json({ environments: [{ name: "production" }] })
      if (path.endsWith("manifest"))
        return Response.json({ manifest: { version: 1 } })
      throw new Error(path)
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ResourceReviewDialog
        open
        onOpenChange={() => {}}
        providers={[
          {
            name: "vercel",
            title: "Vercel",
            cloud: "vercel",
            available: true,
            provisions: [
              {
                kind: "vercel-blob",
                creates: "Blob store",
                settings: [
                  {
                    name: "project_id",
                    title: "Existing Vercel project ID",
                    required: true,
                    pattern: "^prj_[a-z0-9]+$",
                  },
                  {
                    name: "environment",
                    title: "Environment scope",
                    required: true,
                    pattern: "^(production|preview)$",
                    default: "production",
                  },
                ],
              },
            ],
          } as never,
        ]}
        connections={[
          { id: "c", name: "Team", cloud: "vercel", status: "ready" } as never,
        ]}
      />
    </ClientProvider>
  )
  const project = await screen.findByLabelText("Existing Vercel project ID")
  const review = screen.getByRole("button", { name: "Review deployment plan" })
  expect(review).toBeDisabled()
  expect(screen.getByLabelText("Environment scope")).toHaveValue("production")
  await userEvent.type(project, "not-a-project")
  expect(review).toBeDisabled()
  await userEvent.clear(project)
  await userEvent.type(project, "prj_existing")
  expect(review).toBeEnabled()
  await userEvent.click(review)
  await vi.waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[0].body).toEqual({
    add_resource: {
      name: "resource",
      kind: "vercel-blob",
      provider: "vercel",
      target: "c",
      region: "",
      version: "",
      enabled_in: "production",
      settings: { project_id: "prj_existing", environment: "production" },
    },
  })
  expect(writes[1].path).toBe("/api/v1/projects/p/plans")
})
