import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { AddHostnameDialog } from "./add-hostname-dialog"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const zero = "0001-01-01T00:00:00Z"

const domains = [
  {
    zone_id: "zone_dev",
    name: "acme.dev",
    mode: "managed",
    provider: "cloudflare",
    verified: true,
    wildcards: true,
    hostnames: [],
  },
]

const rec = (type: string, value: string) => ({
  id: `${type}-${value}`,
  zone_id: "zone_dev",
  name: "",
  type,
  value,
  ttl: 300,
})

function stub(over: Record<string, unknown> = {}) {
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST")
        return Response.json({
          id: "routeop_1",
          kind: "apply",
          status: "preview",
          review: "token",
          attempt: 1,
          revision: 1,
          generation: 0,
          message: "",
          created_at: zero,
          expires_at: zero,
          not_before: zero,
          dns_attempted: false,
          desired_record: {},
          ...over,
        })
      if (path === "/api/v1/projects/proj/domains")
        return Response.json({ domains })
      return Response.json({}, { status: 404 })
    }
  )
}

function mount(props: { ports?: number[]; blocked?: string } = {}) {
  render(
    <ClientProvider client={forgeClient}>
      <AddHostnameDialog
        projectId="proj"
        environment={{ id: "prod", name: "production" }}
        service="web"
        ports={props.ports ?? [8080]}
        blocked={props.blocked}
        open
        onOpenChange={() => {}}
      />
    </ClientProvider>
  )
}

const preview = async () => {
  await screen.findByRole("combobox", { name: "Domain" })
  await userEvent.click(screen.getByRole("button", { name: "Preview" }))
}

it("lists every record an App Platform apex needs", async () => {
  stub({
    desired_record: rec("A", "162.159.140.98"),
    desired_records: [
      rec("A", "162.159.140.98"),
      rec("A", "172.66.0.96"),
      rec("AAAA", "2606:4700:7::60"),
      rec("AAAA", "2a06:98c1:58::60"),
    ],
  })
  mount()
  await preview()
  const group = await screen.findByRole("group", { name: "DNS records" })
  for (const value of [
    "162.159.140.98",
    "172.66.0.96",
    "2606:4700:7::60",
    "2a06:98c1:58::60",
  ])
    expect(group.textContent).toContain(value)
  expect(group.querySelectorAll("dl")).toHaveLength(4)
})

it("keeps the single record for a server route", async () => {
  stub({ desired_record: rec("CNAME", "web.example.net") })
  mount()
  await preview()
  const group = await screen.findByRole("group", { name: "DNS record" })
  expect(group.querySelectorAll("dl")).toHaveLength(1)
  expect(group.textContent).toContain("web.example.net")
})

it("shows the cost, marking an estimate", async () => {
  stub({
    desired_record: rec("A", "1.1.1.1"),
    cost: { monthly_usd: "18.25", estimate: true, source: "pricing" },
  })
  mount()
  await preview()
  expect(await screen.findByText("$18.25 a month (estimate)")).toBeTruthy()

  cleanup()
  forgeClient.clear()
  stub({
    desired_record: rec("A", "1.1.1.1"),
    cost: { monthly_usd: "18.25", estimate: false, source: "pricing" },
  })
  mount()
  await preview()
  expect(await screen.findByText("$18.25 a month")).toBeTruthy()
})

it("says why a private App Platform service can't take a hostname", () => {
  stub()
  mount({
    ports: [],
    blocked:
      "This service is private on App Platform, so no hostname can reach it. Make it public in the manifest and deploy it first.",
  })
  expect(screen.getByText(/is private on App Platform/)).toBeTruthy()
  expect(screen.queryByRole("combobox", { name: "Port" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Preview" })).toBeNull()
})
