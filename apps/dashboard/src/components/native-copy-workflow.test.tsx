import { ClientProvider } from "@forge-go/client-react"
import { dehydrate } from "@forge-go/client-core"
import { StrictMode } from "react"
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { NativeRecovery } from "@/components/native-recovery"
import { ServiceRecovery } from "@/pages/service-catalog"

const permission = vi.hoisted(() => ({
  manage: true,
  secret: true,
  read: true,
}))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({
    can: (name: string) =>
      (name !== "service_restore:manage" || permission.manage) &&
      (name !== "secret:manage" || permission.secret) &&
      (name !== "service_backup:read" || permission.read),
    session: { userId: "owner", orgId: "org" },
  }),
}))

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  permission.manage = true
  permission.secret = true
  permission.read = true
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function fixture({
  denyReview = false,
  noTargets = false,
  removed = false,
} = {}) {
  const reviews: Record<string, unknown>[] = []
  const creates: Record<string, unknown>[] = []
  const deletes: string[] = []
  const reads: string[] = []
  const credentialSignals: AbortSignal[] = []
  const control = {
    loseCreate: false,
    cleanupFails: true,
    denyHistory: false,
    nextBefore: "",
    denyCredentials: false,
    credentialResponse: null as Promise<Response> | null,
  }
  const source = {
    id: "source",
    org_id: "org",
    project_id: "project",
    environment_id: "prod",
    name: "orders",
    provider: "cloud-sql",
    connection_id: "source-account",
    physical_id: "source-original",
    region: "us-central1",
    state: "ready",
    complete: true,
    captured_at: "2026-10-20T00:00:00Z",
  }
  const point = {
    id: "point",
    source_id: "source",
    provider_id: "projects/source-project/backups/daily",
    name: "daily",
    kind: "ENHANCED",
    status: "SUCCESSFUL",
    engine: "postgres",
    version: "17",
    created_at: "2026-10-20T00:00:00Z",
    expires_at: "2099-01-01T00:00:00Z",
    last_seen_at: "2026-10-20T00:00:00Z",
    present: true,
    encrypted: true,
  }
  const target = {
    id: "recovery",
    cloud_id: "cloud",
    name: "recovery",
    display_name: "Recovery SQL",
    org_id: "org",
    project_id: "project",
    runtime: "managed",
    provider: "cloud-sql",
    region: "europe-west1",
    connection_id: "recovery-account",
    revision: 2,
  }
  const baseCopy = {
    id: "copy",
    org_id: "org",
    project_id: "project",
    environment_id: "prod",
    source_id: "source",
    point_id: "point",
    status: "queued",
    created_by: "owner",
    created_at: "2026-10-23T00:00:00Z",
    expires_at: "2099-01-01T00:00:00Z",
    target: {
      provider: "cloud-sql",
      region: "europe-west1",
      native_project: "recovery-project",
    },
  }
  let copy: Record<string, unknown> | null = null
  const seedCopy = (fields: Record<string, unknown> = {}) => {
    copy = { ...baseCopy, ...fields }
  }
  const reply = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    })
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost")
      const method = init?.method ?? "GET"
      reads.push(`${method} ${url.pathname}${url.search}`)
      if (
        url.pathname === "/api/v1/native-recovery-copies/copy/credentials" &&
        method === "POST"
      ) {
        if (init?.signal) credentialSignals.push(init.signal)
        if (init?.signal?.aborted)
          throw new DOMException("The operation was aborted", "AbortError")
        if (control.credentialResponse) return control.credentialResponse
        return control.denyCredentials
          ? reply({ message: "Reader access could not be checked" }, 503)
          : reply({
              copy_id: "copy",
              provider: "cloud-sql",
              connection_name: "recovery:europe-west1:copy",
              database: "orders",
              username: "lp_recovery_reader",
              password: "private-native-reader",
              expires_at: new Date(Date.now() + 3600000).toISOString(),
              checked_at: new Date().toISOString(),
            })
      }
      if (url.pathname.endsWith("/native-recovery-sources"))
        return reply({
          sources:
            !url.searchParams.get("resource") ||
            url.searchParams.get("resource") === source.name
              ? [source]
              : [],
        })
      if (url.pathname.endsWith("/environments"))
        return reply({
          environments: [
            { id: "prod", name: "production" },
            { id: "staging", name: "staging" },
          ],
        })
      if (url.pathname.endsWith("/bindings"))
        return reply({
          bindings: removed
            ? []
            : [
                {
                  id: "managed-orders",
                  subject: "resource",
                  name: "orders",
                  provider: "cloud-sql",
                  environment_id: "prod",
                  config: {
                    kind: "postgres",
                    provider: "cloud-sql",
                    managed: { profile: { id: "postgresql-17-cloud-sql" } },
                  },
                },
              ],
        })
      if (url.pathname.endsWith("/service-backups"))
        return reply({ backups: [] })
      if (url.pathname.endsWith("/service-restores"))
        return reply({ restores: [] })
      if (url.pathname.endsWith("/backup-policies"))
        return reply({ policies: [] })
      if (url.pathname === "/api/v1/native-recovery-sources/source")
        return reply({ source, points: [point] })
      if (url.pathname === "/api/v1/projects/project/clouds")
        return reply({
          clouds: [{ id: "cloud", name: "Recovery", revision: 1 }],
        })
      if (url.pathname === "/api/v1/projects/project/clouds/cloud/targets")
        return reply({
          targets: noTargets
            ? []
            : [
                target,
                {
                  ...target,
                  id: "rds",
                  display_name: "Unsupported RDS",
                  provider: "rds",
                },
              ],
        })
      if (url.pathname.endsWith("/copy-review")) {
        const body = JSON.parse(String(init?.body))
        reviews.push(body)
        if (denyReview) return reply({ message: "Target grant changed" }, 409)
        return reply({
          request_id: body.request_id,
          review_token: `signed-${body.request_id}`,
          source_id: "source",
          point_id: "point",
          ttl_hours: body.ttl_hours,
          valid_until: new Date(Date.now() + 600000).toISOString(),
          notice:
            "Charges continue until deletion is confirmed. Cleanup can fail; check copy status.",
          target: {
            provider: "cloud-sql",
            cloud_id: "cloud",
            target_id: "recovery",
            region: "europe-west1",
            native_project: "recovery-project",
            connection_id: "recovery-account",
            account_identity: "gcp:recovery-project",
            cloud_revision: 1,
            target_revision: 2,
            grant_revision: 3,
          },
          capacity: {
            profile_id: "cloud-sql-postgres-17",
            profile_revision: "1",
            compute_class: "db-custom-1-3840",
            storage_gb: 10,
            storage_type: "PD_SSD",
            availability: "ZONAL",
          },
        })
      }
      if (
        url.pathname === "/api/v1/native-recovery-copies" &&
        method === "POST"
      ) {
        const body = JSON.parse(String(init?.body))
        creates.push(body)
        copy ??= { ...baseCopy }
        if (control.loseCreate)
          return reply({ message: "Admission response unavailable" }, 503)
        return reply(copy, 202)
      }
      if (
        url.pathname === "/api/v1/native-recovery-copies/copy" &&
        method === "DELETE"
      ) {
        deletes.push(url.pathname)
        copy = {
          ...copy,
          status: control.cleanupFails ? "cleanup_failed" : "deleted",
          error: control.cleanupFails ? "Cloud deletion denied" : "",
        }
        return reply(copy, 202)
      }
      if (url.pathname === "/api/v1/native-recovery-copies/copy")
        return reply(copy)
      if (
        url.pathname.includes("/projects/") &&
        url.pathname.endsWith("/native-recovery-copies")
      ) {
        if (control.denyHistory)
          return reply({ message: "Copy history denied" }, 403)
        return reply({
          copies:
            !url.searchParams.get("before") &&
            url.searchParams.get("environment_id") === "prod" &&
            (!url.searchParams.get("resource") ||
              url.searchParams.get("resource") === "orders") &&
            copy
              ? [copy]
              : [],
          next_before: url.searchParams.get("before") ? "" : control.nextBefore,
        })
      }
      return reply({ message: `Unexpected ${method} ${url.pathname}` }, 404)
    }
  )
  return {
    reviews,
    creates,
    deletes,
    reads,
    control,
    seedCopy,
    credentialSignals,
  }
}

function show(project = "project", environment = "prod") {
  return render(
    <ClientProvider client={forgeClient}>
      <NativeRecovery project={project} environment={environment} />
    </ClientProvider>
  )
}

async function openReview() {
  await userEvent.click(
    await screen.findByRole("button", { name: "Review native copy from daily" })
  )
  const dialog = await screen.findByRole("dialog", {
    name: "Review native copy",
  })
  await userEvent.click(
    within(dialog).getByRole("combobox", { name: "Destination Cloud" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "Recovery" }))
  await userEvent.click(
    within(dialog).getByRole("combobox", { name: "Destination target" })
  )
  await userEvent.click(
    await screen.findByRole("option", { name: /Recovery SQL/ })
  )
  return dialog
}

it.each(["secret", "read"] as const)(
  "reveals native reader credentials only on request and forgets them on close or %s permission loss",
  async (revoked) => {
    const f = fixture()
    f.seedCopy({ status: "succeeded", verified_at: new Date().toISOString() })
    const view = show()
    await screen.findByText("SQL verification recorded")
    expect(f.reads.some((url) => url.endsWith("/credentials"))).toBe(false)
    await userEvent.click(
      screen.getByRole("button", { name: "Reader credentials" })
    )
    let dialog = await screen.findByRole("dialog", {
      name: "Recovery copy credentials",
    })
    expect(
      within(dialog).queryByLabelText("Reader password")
    ).not.toBeInTheDocument()
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Reveal reader credentials" })
    )
    expect(await within(dialog).findByLabelText("Reader password")).toHaveValue(
      "private-native-reader"
    )
    expect(
      JSON.stringify(
        dehydrate(forgeClient, {
          principal: forgeClient.owner as string | undefined,
        })
      )
    ).not.toContain("private-native-reader")
    expect(
      JSON.stringify(
        [...forgeClient.store.keys()].map((key) =>
          forgeClient.store.getRecord(key)
        )
      )
    ).not.toContain("private-native-reader")
    await userEvent.click(within(dialog).getByRole("button", { name: "Done" }))
    expect(screen.queryByLabelText("Reader password")).not.toBeInTheDocument()
    await userEvent.click(
      screen.getByRole("button", { name: "Reader credentials" })
    )
    dialog = await screen.findByRole("dialog", {
      name: "Recovery copy credentials",
    })
    expect(
      within(dialog).queryByLabelText("Reader password")
    ).not.toBeInTheDocument()
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Reveal reader credentials" })
    )
    await within(dialog).findByLabelText("Reader password")
    permission[revoked] = false
    view.rerender(
      <ClientProvider client={forgeClient}>
        <NativeRecovery project="project" environment="prod" />
      </ClientProvider>
    )
    expect(screen.queryByLabelText("Reader password")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Reader credentials" })
    ).not.toBeInTheDocument()
  }
)

it.each(["failed", "unverified", "expired"])(
  "does not offer credentials for a %s native copy",
  async (status) => {
    const f = fixture()
    f.seedCopy({
      status: status === "failed" ? "failed" : "succeeded",
      verified_at:
        status === "unverified" ? undefined : new Date().toISOString(),
      expires_at:
        status === "expired" ? "2020-01-01T00:00:00Z" : "2099-01-01T00:00:00Z",
    })
    show()
    await screen.findByRole("list", { name: "Temporary native copies" })
    expect(
      screen.queryByRole("button", { name: "Reader credentials" })
    ).not.toBeInTheDocument()
  }
)

it("keeps a failed credential check retryable without showing a password", async () => {
  const f = fixture()
  f.seedCopy({ status: "succeeded", verified_at: new Date().toISOString() })
  f.control.denyCredentials = true
  show()
  await userEvent.click(
    await screen.findByRole("button", { name: "Reader credentials" })
  )
  const dialog = await screen.findByRole("dialog", {
    name: "Recovery copy credentials",
  })
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Reveal reader credentials" })
  )
  expect(
    await within(dialog).findByText(/Reader access could not be checked/)
  ).toBeInTheDocument()
  expect(
    within(dialog).queryByLabelText("Reader password")
  ).not.toBeInTheDocument()
  f.control.denyCredentials = false
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Reveal reader credentials" })
  )
  expect(await within(dialog).findByLabelText("Reader password")).toHaveValue(
    "private-native-reader"
  )
})

it("reveals and retries credentials in StrictMode and cancels on close", async () => {
  const f = fixture()
  f.seedCopy({ status: "succeeded", verified_at: new Date().toISOString() })
  render(
    <StrictMode>
      <ClientProvider client={forgeClient}>
        <NativeRecovery project="project" environment="prod" />
      </ClientProvider>
    </StrictMode>
  )
  await userEvent.click(
    await screen.findByRole("button", { name: "Reader credentials" })
  )
  let dialog = await screen.findByRole("dialog", {
    name: "Recovery copy credentials",
  })
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Reveal reader credentials" })
  )
  expect(await within(dialog).findByLabelText("Reader password")).toHaveValue(
    "private-native-reader"
  )
  await userEvent.click(within(dialog).getByRole("button", { name: "Done" }))
  expect(f.credentialSignals.at(-1)?.aborted).toBe(true)
  f.control.denyCredentials = true
  await userEvent.click(
    screen.getByRole("button", { name: "Reader credentials" })
  )
  dialog = await screen.findByRole("dialog", {
    name: "Recovery copy credentials",
  })
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Reveal reader credentials" })
  )
  await within(dialog).findByRole("alert")
  f.control.denyCredentials = false
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Reveal reader credentials" })
  )
  expect(await within(dialog).findByLabelText("Reader password")).toHaveValue(
    "private-native-reader"
  )
})

it("discards a pending credential response after backup-read revocation", async () => {
  const f = fixture()
  f.seedCopy({ status: "succeeded", verified_at: new Date().toISOString() })
  let resolve!: (response: Response) => void
  f.control.credentialResponse = new Promise<Response>((done) => {
    resolve = done
  })
  const view = show()
  await userEvent.click(
    await screen.findByRole("button", { name: "Reader credentials" })
  )
  const dialog = await screen.findByRole("dialog", {
    name: "Recovery copy credentials",
  })
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Reveal reader credentials" })
  )
  permission.read = false
  view.rerender(
    <ClientProvider client={forgeClient}>
      <NativeRecovery project="project" environment="prod" />
    </ClientProvider>
  )
  expect(
    screen.queryByRole("dialog", { name: "Recovery copy credentials" })
  ).not.toBeInTheDocument()
  await act(async () =>
    resolve(
      new Response(
        JSON.stringify({
          copy_id: "copy",
          password: "late-private-reader",
          expires_at: "2099-01-01T00:00:00Z",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      )
    )
  )
  expect(screen.queryByLabelText("Reader password")).not.toBeInTheDocument()
  expect(
    JSON.stringify(
      [...forgeClient.store.keys()].map((key) =>
        forgeClient.store.getRecord(key)
      )
    )
  ).not.toContain("late-private-reader")
})

it("reviews charges and retries the same uncertain admission before tracking cleanup", async () => {
  const f = fixture()
  f.control.loseCreate = true
  show()
  const dialog = await openReview()
  expect(
    screen.queryByRole("option", { name: /Unsupported RDS/ })
  ).not.toBeInTheDocument()
  const hours = within(dialog).getByRole("spinbutton", {
    name: "Lifetime in hours",
  })
  await userEvent.clear(hours)
  await userEvent.type(hours, "2")
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  expect(await within(dialog).findByText(/10 GiB/)).toBeInTheDocument()
  expect(within(dialog).getByText(/recovery-project/)).toBeInTheDocument()
  const create = within(dialog).getByRole("button", {
    name: "Create temporary copy",
  })
  expect(create).toBeDisabled()
  expect(f.creates).toHaveLength(0)
  await userEvent.click(
    within(dialog).getByRole("checkbox", { name: /acknowledge charges/i })
  )
  await userEvent.click(create)
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    /not confirmed|unavailable/i
  )
  expect(hours).toBeDisabled()
  f.control.loseCreate = false
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Retry same request" })
  )
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  )
  expect(f.reviews).toHaveLength(1)
  expect(f.reviews[0]).toMatchObject({
    cloud_id: "cloud",
    target_id: "recovery",
    ttl_hours: 2,
  })
  expect(f.creates.length).toBeGreaterThanOrEqual(2)
  for (const body of f.creates)
    expect(body).toMatchObject({
      request_id: f.reviews[0].request_id,
      review_token: `signed-${f.reviews[0].request_id}`,
      acknowledge_charges: true,
    })
  expect(await screen.findByText("Copy queued")).toBeInTheDocument()
  expect(screen.getByText(/not SQL verified/i)).toBeInTheDocument()
  await userEvent.click(screen.getByRole("button", { name: "Clean up copy" }))
  expect(f.deletes).toHaveLength(0)
  let confirmation = await screen.findByRole("alertdialog")
  await userEvent.click(
    within(confirmation).getByRole("button", { name: "Confirm cleanup" })
  )
  expect(await screen.findByText("Cleanup needs attention")).toBeInTheDocument()
  expect(screen.getByText("Cloud deletion denied")).toBeInTheDocument()
  f.control.cleanupFails = false
  await userEvent.click(screen.getByRole("button", { name: "Retry cleanup" }))
  confirmation = await screen.findByRole("alertdialog")
  await userEvent.click(
    within(confirmation).getByRole("button", { name: "Confirm cleanup" })
  )
  expect(await screen.findByText("Copy deleted")).toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: /clean up copy|retry cleanup/i })
  ).not.toBeInTheDocument()
})

it("shows a changed grant without admitting a copy", async () => {
  const f = fixture({ denyReview: true })
  show()
  const dialog = await openReview()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    /target grant changed/i
  )
  expect(f.creates).toHaveLength(0)
  expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument()
})

it("keeps read-only history available without create or cleanup controls", async () => {
  permission.manage = false
  const f = fixture()
  f.seedCopy({ status: "cleanup_failed", error: "Retention needs cleanup" })
  show()
  await screen.findByText("daily")
  await screen.findByText("Cleanup needs attention")
  expect(
    screen.queryByRole("button", { name: /review native copy|clean up copy/i })
  ).not.toBeInTheDocument()
  expect(f.reads.some((url) => url.includes("/clouds"))).toBe(false)
})

it("expires a review before first submission and requires a fresh acknowledgement", async () => {
  const f = fixture()
  show()
  const dialog = await openReview()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  await within(dialog).findByText(/10 GiB/)
  await userEvent.click(
    within(dialog).getByRole("checkbox", { name: /acknowledge charges/i })
  )
  expect(
    within(dialog).getByRole("button", { name: "Create temporary copy" })
  ).toBeEnabled()
  const future = Date.now() + 660000
  vi.spyOn(Date, "now").mockReturnValue(future)
  await waitFor(
    () =>
      expect(
        within(dialog).getByRole("button", { name: "Create temporary copy" })
      ).toBeDisabled(),
    { timeout: 2500 }
  )
  expect(within(dialog).getByRole("status")).toHaveTextContent(
    /review expired/i
  )
  expect(f.creates).toHaveLength(0)
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  await waitFor(() => expect(f.reviews).toHaveLength(2))
  expect(
    within(dialog).getByRole("checkbox", { name: /acknowledge charges/i })
  ).not.toBeChecked()
  expect(
    within(dialog).getByRole("button", { name: "Create temporary copy" })
  ).toBeDisabled()
})

it("invalidates capacity and acknowledgement when lifetime changes", async () => {
  const f = fixture()
  show()
  const dialog = await openReview()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  await within(dialog).findByText(/10 GiB/)
  await userEvent.click(
    within(dialog).getByRole("checkbox", { name: /acknowledge charges/i })
  )
  const hours = within(dialog).getByRole("spinbutton", {
    name: "Lifetime in hours",
  })
  await userEvent.clear(hours)
  await userEvent.type(hours, "3")
  expect(
    within(dialog).queryByRole("button", { name: "Create temporary copy" })
  ).not.toBeInTheDocument()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  await within(dialog).findByText(/10 GiB/)
  expect(f.reviews[1]).toMatchObject({ ttl_hours: 3 })
  expect(f.reviews[1].request_id).not.toBe(f.reviews[0].request_id)
  expect(
    within(dialog).getByRole("checkbox", { name: /acknowledge charges/i })
  ).not.toBeChecked()
})

it("explains an incompatible granted Cloud with the shared empty state", async () => {
  fixture({ noTargets: true })
  show()
  await userEvent.click(
    await screen.findByRole("button", { name: "Review native copy from daily" })
  )
  const dialog = await screen.findByRole("dialog")
  await userEvent.click(
    within(dialog).getByRole("combobox", { name: "Destination Cloud" })
  )
  await userEvent.click(await screen.findByRole("option", { name: "Recovery" }))
  expect(
    await within(dialog).findByText("No compatible recovery target")
  ).toBeInTheDocument()
  expect(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  ).toBeDisabled()
})

it("keeps a denied copy history distinct from an empty result", async () => {
  const f = fixture()
  f.control.denyHistory = true
  show()
  expect(await screen.findByText(/copy history denied/i)).toBeInTheDocument()
  expect(
    screen.queryByText("No native recovery copies")
  ).not.toBeInTheDocument()
})

it.each([
  { environment: "staging", resource: "" },
  { environment: "prod", resource: "missing" },
])(
  "clears review and copy pagination when scope changes to %s",
  async ({ environment, resource }) => {
    const f = fixture()
    f.seedCopy()
    f.control.nextBefore = "copy"
    const view = show()
    await screen.findByText("Copy queued")
    await userEvent.click(screen.getByRole("button", { name: "More copies" }))
    await waitFor(() =>
      expect(
        f.reads.some(
          (url) =>
            url.includes("before=copy") && url.includes("environment_id=prod")
        )
      ).toBe(true)
    )
    const dialog = await openReview()
    await userEvent.click(
      within(dialog).getByRole("button", {
        name: "Review capacity and charges",
      })
    )
    await within(dialog).findByText(/10 GiB/)
    view.rerender(
      <ClientProvider client={forgeClient}>
        <NativeRecovery
          project="project"
          environment={environment}
          resource={resource}
        />
      </ClientProvider>
    )
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    )
    await screen.findByText("No captured cloud database history")
    expect(screen.queryByText("Copy queued")).not.toBeInTheDocument()
    const lastPage = f.reads
      .filter((url) => url.includes("/native-recovery-copies?"))
      .at(-1)
    expect(lastPage).toContain(`environment_id=${environment}`)
    if (resource) expect(lastPage).toContain(`resource=${resource}`)
    expect(lastPage).not.toContain("before=")
  }
)

it("polls retained status and requires SQL proof before describing a verified copy", async () => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] })
  const f = fixture()
  f.seedCopy()
  show()
  await screen.findByText("Copy queued")
  f.seedCopy({ status: "succeeded" })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000)
  })
  expect(await screen.findByText("SQL proof missing")).toBeInTheDocument()
  expect(screen.getByText(/not SQL verified/i)).toBeInTheDocument()
  expect(
    screen.queryByText("SQL verification recorded")
  ).not.toBeInTheDocument()
  f.seedCopy({ status: "succeeded", verified_at: "2026-10-23T01:00:00Z" })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000)
  })
  expect(
    await screen.findByText("SQL verification recorded")
  ).toBeInTheDocument()
  expect(screen.queryByText(/not SQL verified/i)).not.toBeInTheDocument()
  expect(
    f.reads.filter((url) => url.includes("/native-recovery-copies?")).length
  ).toBeGreaterThanOrEqual(3)
})

it.each([false, true])(
  "opens a chosen managed resource's retained native recovery (removed=%s)",
  async (removed) => {
    const f = fixture({ removed })
    f.seedCopy({ status: "cleanup_failed" })
    render(
      <ClientProvider client={forgeClient}>
        <ServiceRecovery
          project="project"
          environment="prod"
          resource="orders"
        />
      </ClientProvider>
    )
    expect(
      await screen.findByRole("button", {
        name: "Review native copy from daily",
      })
    ).toBeInTheDocument()
    expect(screen.getByText("Cleanup needs attention")).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Back up now" })
    ).not.toBeInTheDocument()
    const sources = f.reads.find((url) =>
      url.includes("/native-recovery-sources?")
    )
    const copies = f.reads.find((url) =>
      url.includes("/native-recovery-copies?")
    )
    expect(sources).toContain("resource=orders")
    expect(sources).toContain("environment_id=prod")
    expect(copies).toContain("resource=orders")
    expect(copies).toContain("environment_id=prod")
  }
)

it("replaces a preselected resource's review and cursor when the default-environment entry changes", async () => {
  const f = fixture()
  f.seedCopy()
  f.control.nextBefore = "copy"
  const view = render(
    <ClientProvider client={forgeClient}>
      <ServiceRecovery project="project" resource="orders" />
    </ClientProvider>
  )
  await screen.findByText("Copy queued")
  await userEvent.click(screen.getByRole("button", { name: "More copies" }))
  await waitFor(() =>
    expect(
      f.reads.some(
        (url) => url.includes("before=copy") && url.includes("resource=orders")
      )
    ).toBe(true)
  )
  const dialog = await openReview()
  await userEvent.click(
    within(dialog).getByRole("button", { name: "Review capacity and charges" })
  )
  await within(dialog).findByText(/10 GiB/)
  await userEvent.click(
    within(dialog).getByRole("checkbox", { name: /acknowledge charges/i })
  )
  view.rerender(
    <ClientProvider client={forgeClient}>
      <ServiceRecovery project="project" resource="analytics" />
    </ClientProvider>
  )
  expect(
    await screen.findByText("Cloud snapshots for analytics")
  ).toBeInTheDocument()
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  expect(
    screen.queryByRole("button", { name: "Review native copy from daily" })
  ).not.toBeInTheDocument()
  const lastPage = f.reads
    .filter((url) => url.includes("/native-recovery-copies?"))
    .at(-1)
  expect(lastPage).toContain("resource=analytics")
  expect(lastPage).not.toContain("before=")
  expect(f.creates).toHaveLength(0)
})
