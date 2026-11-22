import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor, cleanup } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { it, expect, vi, afterEach } from "vitest"
import { forgeClient } from "@/client/runtime"
import { ScaleResourceDialog } from "./scale-dialog"
const navigate = vi.fn()
vi.mock("@/lib/router", () => ({ useNavigate: () => navigate }))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})
it("loads capacity for the bound account and reviews the environment-specific resize", async () => {
  const user = userEvent.setup()
  const writes: Record<string, unknown>[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/bindings"))
        return Response.json({
          bindings: [
            {
              name: "db",
              subject: "resource",
              provider: "digitalocean",
              connection_id: "richmond",
              region: "nyc3",
              external_id: "cluster",
            },
          ],
        })
      if (path.endsWith("/richmond/digitalocean-database-capacity"))
        return Response.json({
          pg: {
            regions: ["nyc3"],
            layouts: [
              { num_nodes: 1, sizes: ["small", "large"] },
              { num_nodes: 2, sizes: ["large"] },
            ],
          },
        })
      if (init?.body) writes.push(JSON.parse(String(init.body)))
      if (path.endsWith("/manifest/edits"))
        return Response.json({ revision: 4, yaml: "reviewed" })
      if (path.endsWith("/manifest"))
        return Response.json({ revision: 5, yaml: "reviewed" })
      if (path.endsWith("/plans")) return Response.json({ id: "scale-plan" })
      throw new Error(path)
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ScaleResourceDialog
        projectId="owner"
        name="db"
        env="production"
        revision={4}
        manifest={
          {
            resources: {
              db: { kind: "postgres", settings: { size: "small", nodes: "1" } },
            },
          } as never
        }
        provider={
          {
            name: "digitalocean",
            title: "DigitalOcean",
            provisions: [
              {
                kind: "postgres",
                settings: [
                  {
                    capacity: true,
                    name: "size",
                    title: "Size per node",
                    default: "small",
                  },
                  {
                    capacity: true,
                    name: "nodes",
                    title: "Total nodes",
                    default: "1",
                  },
                ],
              },
            ],
          } as never
        }
        region=""
        close={() => {}}
        saved={() => {}}
      />
    </ClientProvider>
  )
  await screen.findByLabelText("Size per node (optional)")
  expect(
    screen.getByRole("button", { name: "Review deployment plan" })
  ).toBeDisabled()
  await user.selectOptions(
    screen.getByLabelText("Size per node (optional)"),
    "large"
  )
  await user.selectOptions(screen.getByLabelText("Total nodes (optional)"), "2")
  await user.click(
    screen.getByRole("button", { name: "Review deployment plan" })
  )
  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: "/deployments/$id",
      params: { id: "scale-plan" },
    })
  )
  expect(writes).toEqual([
    {
      dry_run: true,
      update_resource: {
        resource: "db",
        environment: "production",
        settings: { size: "large", nodes: "2" },
      },
    },
    { base_revision: 4, yaml: "reviewed" },
    { environment: "production", catalog: true },
  ])
})
