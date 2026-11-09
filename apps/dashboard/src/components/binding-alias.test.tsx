import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { aliasOf, BindingAlias } from "@/components/binding-alias"
import { binding } from "@/components/fabriq/test-api"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function serve(status = 200) {
  const patches: { url: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost").pathname
      const body = JSON.parse(String(init?.body ?? "{}")) as { alias: string }
      patches.push({ url, body })
      const reply =
        status === 200
          ? { id: "b_api", name: "api", alias: body.alias }
          : {
              message:
                "another service or resource in this environment already has that alias",
            }
      return new Response(JSON.stringify(reply), {
        status,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return patches
}

function renderAlias(extra = {}) {
  render(
    <ClientProvider client={forgeClient}>
      <BindingAlias
        projectId="p"
        environment="production"
        binding={binding("api", { suggestedAlias: "shop-api", ...extra })}
      />
    </ClientProvider>
  )
}

it("shows the alias, or the manifest name when there is none", () => {
  expect(aliasOf(binding("api"))).toBe("api")
  expect(aliasOf(binding("api", { alias: "storefront" }))).toBe("storefront")
})

it("starts from the suggestion and saves the alias", async () => {
  const patches = serve()
  renderAlias()
  await userEvent.click(
    screen.getByRole("button", { name: "Name api in production" })
  )
  const field = screen.getByLabelText("Alias")
  expect(field).toHaveValue("shop-api")
  await userEvent.clear(field)
  await userEvent.type(field, "Storefront")
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  expect(patches).toEqual([
    { url: "/api/v1/projects/p/bindings/b_api", body: { alias: "storefront" } },
  ])
})

it("offers the suggestion back and clears a set alias", async () => {
  const patches = serve()
  renderAlias({ alias: "storefront" })
  await userEvent.click(
    screen.getByRole("button", { name: "Name api in production" })
  )
  expect(screen.getByLabelText("Alias")).toHaveValue("storefront")
  await userEvent.click(screen.getByRole("button", { name: "Use shop-api" }))
  expect(screen.getByLabelText("Alias")).toHaveValue("shop-api")
  await userEvent.click(screen.getByRole("button", { name: "Clear" }))
  expect(patches.map((p) => p.body)).toEqual([{ alias: "" }])
})

it("says why a taken alias wasn't saved", async () => {
  serve(409)
  renderAlias()
  await userEvent.click(
    screen.getByRole("button", { name: "Name api in production" })
  )
  await userEvent.click(screen.getByRole("button", { name: "Save" }))
  expect(await screen.findByText(/already has that alias/)).toBeInTheDocument()
})
