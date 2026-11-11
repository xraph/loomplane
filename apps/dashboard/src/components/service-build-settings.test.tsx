import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ServiceBuildSettings } from "./service-build-settings"

let allowed = true
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => allowed }),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  allowed = true
})

function show(entry: Record<string, unknown> = {}) {
  const writes: unknown[] = []
  vi.stubGlobal(
    "fetch",
    async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") {
        writes.push(JSON.parse(String(init.body)))
        return Response.json({ revision: 2 }, { status: 201 })
      }
      return Response.json({
        revision: 1,
        yaml: "",
        manifest: {
          version: 1,
          services: { web: { provider: "vercel", ...entry } },
        },
      })
    }
  )
  render(
    <ClientProvider client={forgeClient}>
      <ServiceBuildSettings
        project={{ id: "p" } as never}
        name="web"
        environments={[{ id: "e", name: "production" }] as never}
        deployed={{}}
      />
    </ClientProvider>
  )
  return writes
}

it("saves custom and skipped commands in the selected environment", async () => {
  const user = userEvent.setup()
  const writes = show()
  await user.selectOptions(
    await screen.findByRole("combobox", { name: "Install command mode" }),
    "skip"
  )
  await user.selectOptions(
    screen.getByRole("combobox", { name: "Build command mode" }),
    "custom"
  )
  await user.type(
    screen.getByRole("textbox", { name: "Build command" }),
    "pnpm build"
  )
  await user.click(screen.getByRole("button", { name: "Save build settings" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        placement: {
          section: "services",
          name: "web",
          environment: "production",
          commands: { install: "", build: "pnpm build" },
          vercel: { services: false },
        },
      },
    ])
  )
})

it("saves per-service commands without dropping other members", async () => {
  const user = userEvent.setup()
  const writes = show({
    vercel: {
      services: true,
      root: ".",
      service_commands: { api: { build: "go build" } },
    },
  })
  await user.type(
    await screen.findByRole("combobox", { name: "Vercel service name" }),
    "web"
  )
  await user.selectOptions(
    screen.getByRole("combobox", { name: "Install command mode" }),
    "skip"
  )
  await user.click(screen.getByRole("button", { name: "Save build settings" }))
  await waitFor(() =>
    expect(writes).toEqual([
      {
        placement: {
          section: "services",
          name: "web",
          environment: "production",
          commands: {},
          vercel: {
            services: true,
            root: ".",
            service_commands: {
              api: { build: "go build" },
              web: { install: "" },
            },
          },
        },
      },
    ])
  )
})

it("disables changes without project update permission", async () => {
  allowed = false
  show()
  expect(
    await screen.findByRole("button", { name: "Save build settings" })
  ).toBeDisabled()
  expect(screen.getByRole("checkbox")).toHaveAttribute("aria-disabled", "true")
})
