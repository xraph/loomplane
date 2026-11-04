import { ClientProvider } from "@forge-go/client-react"
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { dehydrate } from "@forge-go/client-core"
import { forgeClient } from "@/client/runtime"
import { PlatformCloudHosts } from "@/components/platform-cloud-hosts"

const toasts = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock("sonner", () => ({ toast: toasts }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  forgeClient.clear()
})

const evidence = (over: Record<string, unknown> = {}) => ({
  isolation: true,
  platform: true,
  smt_active: false,
  denied_ports: [25, 465, 587],
  per_org_vms: 2,
  per_org_vcpus: 2,
  per_org_memory_mib: 2048,
  protocol_version: "1",
  checked_at: new Date().toISOString(),
  ...over,
})

const host = (over: Record<string, unknown> = {}) => ({
  id: "h1",
  host_id: "host-one",
  endpoint: "h1.example.com:8443",
  region_id: "r1",
  status: "ready",
  placements: 0,
  generation: 1,
  revision: 1,
  client_fingerprint: "fp-list",
  allotment: { vcpus: 8, memory_mib: 32768, disk_mib: 204800, vms: 20 },
  use: { vcpus: 1, memory_mib: 1024, disk_mib: 1024, vms: 1 },
  evidence: evidence(),
  ...over,
})

function stub(
  hosts: unknown[],
  onPost?: (path: string) => Response | Promise<Response> | undefined
) {
  const posts: string[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST") {
        posts.push(path)
        const r = await onPost?.(path)
        if (r) return r
      }
      if (path.includes("cloud/regions"))
        return Response.json({
          regions: [
            {
              id: "r1",
              name: "eu",
              display_name: "Europe",
              status: "active",
              revision: 1,
            },
          ],
        })
      return Response.json({ hosts })
    })
  )
  return posts
}

function renderHosts() {
  return render(
    <ClientProvider client={forgeClient}>
      <PlatformCloudHosts />
    </ClientProvider>
  )
}

it("shows each failing check on a host", async () => {
  stub([host({ evidence: evidence({ smt_active: true }) })])
  renderHosts()
  expect(await screen.findByText("SMT is on")).toBeTruthy()
})

it("shows nothing extra for a healthy host", async () => {
  stub([host()])
  renderHosts()
  await screen.findByText("host-one")
  expect(screen.queryByText("SMT is on")).toBeNull()
})

it("shows the client certificate once", async () => {
  const cert = "-----BEGIN CERTIFICATE-----SECRETCERT"
  const fp = "sha256:SECRETFP"
  stub([host()], (path) =>
    path.endsWith("/platform/cloud/hosts")
      ? Response.json({
          client_certificate: cert,
          client_fingerprint: fp,
          host: host({ id: "h2" }),
        })
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  await screen.findByText("host-one")
  await user.click(screen.getByRole("button", { name: "Add host" }))
  const dialog = await screen.findByRole("dialog")
  await user.click(within(dialog).getByRole("combobox", { name: "Region" }))
  await user.click(await screen.findByRole("option", { name: "Europe" }))
  await user.type(
    within(dialog).getByLabelText("Endpoint"),
    "h2.example.com:8443"
  )
  await user.type(within(dialog).getByLabelText("Host ID"), "host-two")
  await user.type(within(dialog).getByLabelText("Server CA"), "CA PEM")
  await user.click(within(dialog).getByRole("button", { name: "Add host" }))
  expect(await within(dialog).findByText(fp)).toBeTruthy()
  expect(within(dialog).getByText(/run Check on the host/)).toBeTruthy()
  expect(within(dialog).getByDisplayValue(cert)).toBeTruthy()
  expect(
    within(dialog).getByRole("button", { name: "Copy certificate" })
  ).toBeTruthy()
  expect(
    within(dialog).getByRole("button", { name: "Copy fingerprint" })
  ).toBeTruthy()

  const cached = JSON.stringify(
    dehydrate(forgeClient, { principal: undefined })
  )
  expect(cached).not.toContain("SECRETCERT")
  expect(cached).not.toContain("SECRETFP")
  expect(cached).toContain("host-one")

  await user.click(within(dialog).getByRole("button", { name: "Done" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(document.body.innerHTML).not.toContain("SECRETCERT")
  expect(document.body.innerHTML).not.toContain("SECRETFP")

  await user.click(screen.getByRole("button", { name: "Add host" }))
  const again = await screen.findByRole("dialog")
  expect(again.innerHTML).not.toContain("SECRETCERT")
  expect(again.innerHTML).not.toContain("SECRETFP")
})

it("asks before retiring", async () => {
  let retired = false
  stub([host({ status: "draining" })], (path) => {
    if (path.endsWith("/retire")) {
      retired = true
      return new Response(null, { status: 204 })
    }
  })
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Retire host-one" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(within(dialog).getByText("Retire host-one?")).toBeTruthy()
  expect(retired).toBe(false)
  await user.click(within(dialog).getByRole("button", { name: "Retire" }))
  await waitFor(() => expect(retired).toBe(true))
})

it("disables retire while a host has workloads", async () => {
  stub([host({ status: "draining", placements: 2 })])
  renderHosts()
  const btn = (await screen.findByRole("button", {
    name: "Retire host-one",
  })) as HTMLButtonElement
  expect(btn.disabled).toBe(true)
  expect(screen.getByText("Move or remove its 2 workloads first")).toBeTruthy()
})

it("uses the singular for one workload", async () => {
  stub([host({ status: "draining", placements: 1 })])
  renderHosts()
  expect(
    await screen.findByText("Move or remove its 1 workload first")
  ).toBeTruthy()
})

it("says to add a region first when there are none", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input instanceof Request ? input.url : input)
      return path.includes("cloud/regions")
        ? Response.json({ regions: [] })
        : Response.json({ hosts: [] })
    })
  )
  const user = userEvent.setup()
  renderHosts()
  await user.click(await screen.findByRole("button", { name: "Add host" }))
  const dialog = await screen.findByRole("dialog")
  expect(await within(dialog).findByText("Add a region first")).toBeTruthy()
  await user.type(within(dialog).getByLabelText("Endpoint"), "e:1")
  await user.type(within(dialog).getByLabelText("Host ID"), "h")
  expect(
    (
      within(dialog).getByRole("button", {
        name: "Add host",
      }) as HTMLButtonElement
    ).disabled
  ).toBe(true)
})

it("warns when an allotment drops below use", async () => {
  stub([host()])
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Edit allotment for host-one" })
  )
  const dialog = await screen.findByRole("dialog")
  const note =
    "Running work keeps running. New work is refused until use is under the limit."
  expect(within(dialog).queryByText(note)).toBeNull()
  const v = within(dialog).getByLabelText("vCPUs")
  await user.clear(v)
  await user.type(v, "0")
  expect(within(dialog).getByText(note)).toBeTruthy()
})

async function addHostUpToSubmit() {
  const user = userEvent.setup()
  renderHosts()
  await screen.findByText("host-one")
  await user.click(screen.getByRole("button", { name: "Add host" }))
  const dialog = await screen.findByRole("dialog")
  await user.click(within(dialog).getByRole("combobox", { name: "Region" }))
  await user.click(await screen.findByRole("option", { name: "Europe" }))
  await user.type(within(dialog).getByLabelText("Endpoint"), "h2:8443")
  await user.type(within(dialog).getByLabelText("Host ID"), "host-two")
  const add = within(dialog).getByRole("button", {
    name: "Add host",
  }) as HTMLButtonElement
  return { user, dialog, add }
}

it("needs a server CA before adding a host", async () => {
  stub([host()])
  const { user, dialog, add } = await addHostUpToSubmit()
  expect(add.disabled).toBe(true)
  await user.type(within(dialog).getByLabelText("Server CA"), "CA PEM")
  expect(add.disabled).toBe(false)
})

it("keeps the certificate on screen through Escape and outside clicks", async () => {
  const cert = "-----BEGIN CERTIFICATE-----KEEPME"
  stub([host()], (path) =>
    path.endsWith("/platform/cloud/hosts")
      ? Response.json({
          client_certificate: cert,
          client_fingerprint: "sha256:KEEPFP",
          host: host({ id: "h2" }),
        })
      : undefined
  )
  const { user, dialog, add } = await addHostUpToSubmit()
  await user.type(within(dialog).getByLabelText("Server CA"), "CA PEM")
  await user.click(add)
  expect(await within(dialog).findByDisplayValue(cert)).toBeTruthy()
  await user.keyboard("{Escape}")
  await user.click(document.body)
  expect(screen.getByRole("dialog")).toBeTruthy()
  expect(screen.getByDisplayValue(cert)).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Close" })).toBeNull()
  await user.click(screen.getByRole("button", { name: "Done" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
})

it("asks before draining and says it can't be undone", async () => {
  const posts = stub([host()], (path) =>
    path.endsWith("/drain")
      ? Response.json(host({ status: "draining" }))
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Drain host-one" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(
    within(dialog).getByText(
      "The host stops taking new work. This can't be undone here."
    )
  ).toBeTruthy()
  expect(posts).toEqual([])
  await user.click(within(dialog).getByRole("button", { name: "Drain" }))
  await waitFor(() =>
    expect(posts.some((p) => p.endsWith("/drain"))).toBe(true)
  )
})

it("says Move all drains the host too", async () => {
  stub([host()])
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Move all from host-one" })
  )
  const dialog = await screen.findByRole("alertdialog")
  expect(within(dialog).getByText(/This can't be undone here\./)).toBeTruthy()
})

it("reports moved, skipped and failed workloads in one toast", async () => {
  let answer: (r: Response) => void = () => undefined
  const posts = stub([host({ status: "draining", placements: 4 })], (path) =>
    path.endsWith("/move-all")
      ? new Promise<Response>((resolve) => {
          answer = resolve
        })
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Move all from host-one" })
  )
  await user.click(
    within(await screen.findByRole("alertdialog")).getByRole("button", {
      name: "Move all",
    })
  )
  await waitFor(() => expect(posts).toHaveLength(1))
  const pending = (await screen.findByRole("button", {
    name: "Move all from host-one",
  })) as HTMLButtonElement
  await waitFor(() => expect(pending.disabled).toBe(true))
  expect(
    screen.getByText("Moving workloads. This can take several minutes.")
  ).toBeTruthy()
  answer(
    Response.json({
      host: host({ status: "draining" }),
      moved: 1,
      skipped: ["api", "web"],
      failed: ["worker"],
    })
  )
  await waitFor(() => expect(toasts.error).toHaveBeenCalledTimes(1))
  expect(toasts.error).toHaveBeenCalledWith(
    "1 moved. 2 stayed for lack of room: api, web. 1 couldn't be moved: worker"
  )
  expect(toasts.success).not.toHaveBeenCalled()
  expect(toasts.warning).not.toHaveBeenCalled()
  await waitFor(() => expect(pending.disabled).toBe(false))
})

it("warns when Move all leaves some workloads for lack of room", async () => {
  stub([host({ status: "draining", placements: 2 })], (path) =>
    path.endsWith("/move-all")
      ? Response.json({
          host: host({ status: "draining" }),
          moved: 0,
          skipped: ["api"],
          failed: [],
        })
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Move all from host-one" })
  )
  await user.click(
    within(await screen.findByRole("alertdialog")).getByRole("button", {
      name: "Move all",
    })
  )
  await waitFor(() =>
    expect(toasts.warning).toHaveBeenCalledWith(
      "0 moved. 1 stayed for lack of room: api"
    )
  )
})

it("lets only a draining host be retired", async () => {
  stub([host()])
  renderHosts()
  const btn = (await screen.findByRole("button", {
    name: "Retire host-one",
  })) as HTMLButtonElement
  expect(btn.disabled).toBe(true)
  expect(screen.getByText("Drain it first")).toBeTruthy()
})

it("offers no actions on a retired host", async () => {
  stub([host({ status: "retired" })])
  renderHosts()
  expect(await screen.findByText("Retired")).toBeTruthy()
  for (const name of [
    "Check host-one",
    "Drain host-one",
    "Move all from host-one",
    "Edit allotment for host-one",
    "Retire host-one",
  ])
    expect(screen.queryByRole("button", { name })).toBeNull()
})

it("says a host was never checked instead of listing failures", async () => {
  stub([
    host({
      status: "pending",
      evidence: {
        isolation: false,
        platform: false,
        smt_active: false,
        denied_ports: [],
        per_org_vms: 0,
        per_org_vcpus: 0,
        per_org_memory_mib: 0,
        protocol_version: "",
        checked_at: "0001-01-01T00:00:00Z",
      },
    }),
  ])
  renderHosts()
  expect(await screen.findByText("Not checked yet")).toBeTruthy()
  expect(screen.getByText("Not ready")).toBeTruthy()
  expect(screen.queryByText("Isolation checks failed")).toBeNull()
})

it("disables Check while it runs", async () => {
  let answer: (r: Response) => void = () => undefined
  stub([host()], (path) =>
    path.endsWith("/check")
      ? new Promise<Response>((resolve) => {
          answer = resolve
        })
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  const btn = (await screen.findByRole("button", {
    name: "Check host-one",
  })) as HTMLButtonElement
  await user.click(btn)
  await waitFor(() => expect(btn.disabled).toBe(true))
  answer(Response.json(host()))
  await waitFor(() => expect(btn.disabled).toBe(false))
})

it("drops the platformcloud prefix from server errors", async () => {
  stub([host()], (path) =>
    path.endsWith("/check")
      ? Response.json(
          { message: "platformcloud: the host is retired" },
          { status: 409 }
        )
      : undefined
  )
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Check host-one" })
  )
  expect(await screen.findByText("The host is retired.")).toBeTruthy()
})

it("blocks saving an allotment with a blank field and keeps untouched memory", async () => {
  const sent: unknown[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method && init.method !== "GET") {
        sent.push(JSON.parse(String(init.body)))
        return Response.json(host())
      }
      if (path.includes("cloud/regions")) return Response.json({ regions: [] })
      return Response.json({
        hosts: [
          host({
            allotment: {
              vcpus: 8,
              memory_mib: 1536,
              disk_mib: 204800,
              vms: 20,
            },
          }),
        ],
      })
    })
  )
  const user = userEvent.setup()
  renderHosts()
  await user.click(
    await screen.findByRole("button", { name: "Edit allotment for host-one" })
  )
  const dialog = await screen.findByRole("dialog")
  const save = within(dialog).getByRole("button", {
    name: "Save",
  }) as HTMLButtonElement
  const v = within(dialog).getByLabelText("vCPUs")
  await user.clear(v)
  expect(within(dialog).getByText("Enter a whole number")).toBeTruthy()
  expect(save.disabled).toBe(true)
  await user.type(v, "6")
  await user.click(save)
  await waitFor(() => expect(sent).toHaveLength(1))
  expect(sent[0]).toMatchObject({ vcpus: 6, memory_mib: 1536 })
})
