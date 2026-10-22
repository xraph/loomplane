import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { HostnameField } from "./hostname-field"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const status = {
  backend: "server",
  certificate: "ready",
  certificate_expires_at: "2027-01-01T00:00:00Z",
  checked_at: "2026-10-22T00:00:00Z",
  dns: "ready",
  hostname: "api.acme.dev",
  retryable: false,
  route: "ready",
  route_id: "route_api",
  wildcard: false,
}

const domains = [
  {
    zone_id: "zone_dev",
    name: "acme.dev",
    mode: "managed",
    provider: "cloudflare",
    verified: true,
    wildcards: true,
    hostnames: [
      {
        route_id: "route_api",
        hostname: "api.acme.dev",
        port: 8080,
        project_id: "proj",
        project_name: "Shop",
        environment_id: "prod",
        service: "web",
        runs_on: "web-1",
        status,
      },
    ],
  },
  {
    zone_id: "zone_org",
    name: "acme.org",
    mode: "external",
    verified: true,
    wildcards: false,
    hostnames: [],
  },
  {
    zone_id: "zone_new",
    name: "pending.dev",
    mode: "external",
    verified: false,
    wildcards: false,
    hostnames: [],
  },
]

function stub(response: () => Response = () => Response.json({ domains })) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname
    if (path === "/api/v1/projects/proj/domains") return response()
    return Response.json({}, { status: 404 })
  })
}

function mount(allowWildcard?: boolean) {
  const changes: { zoneId: string; hostname: string }[] = []
  function Harness() {
    const [value, setValue] = useState({ zoneId: "", hostname: "" })
    return (
      <HostnameField
        projectId="proj"
        value={value}
        allowWildcard={allowWildcard}
        onChange={(next) => {
          changes.push(next)
          setValue(next)
        }}
      />
    )
  }
  render(
    <ClientProvider client={forgeClient}>
      <Harness />
    </ClientProvider>
  )
  return changes
}

const hostname = () => screen.getByTestId("resulting-hostname").textContent

it("offers * only on zones that allow wildcards", async () => {
  stub()
  const changes = mount()
  const domain = await screen.findByRole("combobox", { name: "Domain" })
  // The first verified domain is picked, at its apex.
  expect(changes.at(-1)).toEqual({ zoneId: "zone_dev", hostname: "acme.dev" })
  expect(hostname()).toBe("acme.dev")
  // Unverified domains can't take a hostname yet.
  expect(
    [...domain.querySelectorAll("option")].map((o) => o.textContent)
  ).toEqual(["acme.dev", "acme.org"])

  await userEvent.click(screen.getByRole("button", { name: "Every subdomain" }))
  expect(changes.at(-1)).toEqual({
    zoneId: "zone_dev",
    hostname: "*.acme.dev",
  })
  expect(hostname()).toBe("*.acme.dev")
  expect(
    screen
      .getByRole("button", { name: "Every subdomain" })
      .getAttribute("aria-pressed")
  ).toBe("true")

  // acme.org has no wildcard certificates, so the wildcard goes.
  await userEvent.selectOptions(domain, "zone_org")
  expect(changes.at(-1)).toEqual({ zoneId: "zone_org", hostname: "acme.org" })
  expect(screen.queryByRole("button", { name: "Every subdomain" })).toBeNull()
  await userEvent.type(screen.getByRole("textbox", { name: "Subdomain" }), "*")
  expect(
    screen.getByText(
      "Wildcards on acme.org need wildcard certificates, which are set up on the domain's card on the Domains page."
    )
  ).toBeTruthy()

  cleanup()
  forgeClient.clear()
  stub()
  mount(false)
  await screen.findByRole("combobox", { name: "Domain" })
  expect(screen.queryByRole("button", { name: "Every subdomain" })).toBeNull()
})

it("shows the resulting hostname and whether it's already in use", async () => {
  stub()
  const changes = mount()
  const label = await screen.findByRole("textbox", { name: "Subdomain" })
  await userEvent.type(label, "API")
  expect(changes.at(-1)).toEqual({
    zoneId: "zone_dev",
    hostname: "api.acme.dev",
  })
  expect(hostname()).toBe("api.acme.dev")
  expect(screen.getByText("Already in use by Shop / prod / web")).toBeTruthy()
  await userEvent.type(label, "2")
  expect(hostname()).toBe("api2.acme.dev")
  expect(screen.queryByText(/Already in use/)).toBeNull()
})

it("keeps loading, denied access and a failed request apart", async () => {
  stub(() => Response.json({ message: "forbidden" }, { status: 403 }))
  mount()
  expect(
    await screen.findByText("You can't see this project's domains.")
  ).toBeTruthy()
  cleanup()
  forgeClient.clear()

  stub(() => Response.json({ message: "boom" }, { status: 500 }))
  mount()
  expect(await screen.findByRole("button", { name: "Retry" })).toBeTruthy()
  cleanup()
  forgeClient.clear()

  stub(() => Response.json({ domains: [domains[2]] }))
  mount()
  expect(
    await screen.findByText(
      "No verified domain is granted to this project yet. Grant one from the Domains page."
    )
  ).toBeTruthy()
})
