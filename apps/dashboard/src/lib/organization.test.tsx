import { ClientProvider } from "@forge-go/client-react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { useProjectsList, useSessionGet } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { forgeClient, ORG_HEADER, setTabOrganization } from "@/client/runtime"
import { OrganizationGate, useOrganization } from "./organization"

afterEach(() => {
  cleanup()
  forgeClient.setPrincipal("")
  setTabOrganization("")
  window.localStorage.clear()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it("commits principal synchronization before child reads and abandons late responses across organization and user changes", async () => {
  let org = "A"
  let user = "user1"
  let deferRows = false
  let late: ((value: Response) => void) | undefined
  const requests: { header: string | null; org: string; user: string }[] = []
  const renders: string[] = []
  const warnings = vi
    .spyOn(console, "error")
    .mockImplementation(() => undefined)
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/session"))
        return Response.json({ user_id: user, org_id: org, permissions: [] })
      if (path.endsWith("/orgs"))
        return Response.json({
          organizations: [
            { id: "A", name: "A" },
            { id: "B", name: "B" },
          ],
        })
      if (path.endsWith("/switch-org")) {
        org = "B"
        return Response.json({})
      }
      if (path.endsWith("/projects")) {
        requests.push({
          header: new Headers(init?.headers).get(ORG_HEADER),
          org,
          user,
        })
        if (deferRows) {
          deferRows = false
          return new Promise<Response>((resolve) => {
            late = resolve
          })
        }
        return Response.json({
          projects: [
            { id: "same-id", name: `${user}-${org}`, repositories: [] },
          ],
        })
      }
      throw new Error(`Unexpected request: ${path}`)
    }
  )
  function Child() {
    const { session, switchOrganization } = useOrganization()
    const rows = useQuery(useProjectsList)
    const names = rows.data?.projects.map((p) => p.name).join(",") ?? "loading"
    renders.push(`${session.userId}:${session.orgId}:${names}`)
    return (
      <>
        <p>
          {session.userId}:{session.orgId}:{names}
        </p>
        <button onClick={() => void switchOrganization("B")}>Switch</button>
      </>
    )
  }
  render(
    <ClientProvider client={forgeClient}>
      <OrganizationGate fallback={() => <p>Gated</p>}>
        <Child />
      </OrganizationGate>
    </ClientProvider>
  )
  await screen.findByText("user1:A:user1-A")
  expect(forgeClient.owner).toBe("user1:A")
  deferRows = true
  let pending: Promise<unknown>
  await act(async () => {
    pending = useProjectsList()
      .refetch()
      .catch((error: Error) => error.message)
    await Promise.resolve()
  })
  await waitFor(() => expect(late).toBeDefined())
  await userEvent.setup().click(screen.getByRole("button", { name: "Switch" }))
  await screen.findByText("user1:B:user1-B")
  await act(async () => {
    late!(
      Response.json({
        projects: [{ id: "same-id", name: "late-A", repositories: [] }],
      })
    )
    expect(await pending!).toMatch(/request abandoned/)
  })
  expect(screen.queryByText(/late-A/)).not.toBeInTheDocument()
  user = "user2"
  await act(async () => {
    await useSessionGet().refetch()
  })
  await screen.findByText("user2:B:user2-B")
  expect(forgeClient.owner).toBe("user2:B")
  expect(requests.every((r) => r.header === r.org)).toBe(true)
  expect(
    renders.some((r) => r.startsWith("user1:B:") && /user1-A|late-A/.test(r))
  ).toBe(false)
  expect(
    renders.some((r) => r.startsWith("user2:B:") && /user1-/.test(r))
  ).toBe(false)
  expect(
    warnings.mock.calls.filter((args) =>
      /while rendering|Cannot update/.test(args.join(" "))
    )
  ).toEqual([])
})

it("gates automatic initial activation and removes cached children on session failure", async () => {
  let active = false
  let failed = false
  let switches = 0
  const childHeaders: (string | null)[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/session")) {
        if (failed || !active)
          return Response.json(
            { message: "No active organization" },
            { status: failed ? 409 : 403 }
          )
        return Response.json({ user_id: "user", org_id: "A", permissions: [] })
      }
      if (path.endsWith("/orgs"))
        return Response.json({ organizations: [{ id: "A", name: "A" }] })
      if (path.endsWith("/switch-org")) {
        switches++
        active = true
        return Response.json({})
      }
      if (path.endsWith("/projects")) {
        childHeaders.push(new Headers(init?.headers).get(ORG_HEADER))
        return Response.json({ projects: [] })
      }
      throw new Error(path)
    }
  )
  function Child() {
    useQuery(useProjectsList)
    return <p>Active child</p>
  }
  render(
    <ClientProvider client={forgeClient}>
      <OrganizationGate fallback={(state) => <p>{state.kind}</p>}>
        <Child />
      </OrganizationGate>
    </ClientProvider>
  )
  expect(screen.queryByText("Active child")).not.toBeInTheDocument()
  await screen.findByText("Active child")
  expect(switches).toBe(1)
  await waitFor(() => expect(childHeaders).toEqual(["A"]))
  failed = true
  await act(async () => {
    await useSessionGet()
      .refetch()
      .catch(() => undefined)
  })
  expect(await screen.findByText("error")).toBeInTheDocument()
  expect(screen.queryByText("Active child")).not.toBeInTheDocument()
})

it("follows the workspace an address names and reports one the person isn't in", async () => {
  let org = "A"
  const switched: string[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/session"))
        return Response.json({ user_id: "user", org_id: org, permissions: [] })
      if (path.endsWith("/orgs"))
        return Response.json({
          organizations: [
            { id: "A", name: "Acme", slug: "acme" },
            { id: "B", name: "Bolt", slug: "bolt" },
          ],
        })
      if (path.endsWith("/switch-org")) {
        org = JSON.parse(String(init?.body)).org_id
        switched.push(org)
        return Response.json({})
      }
      throw new Error(path)
    }
  )
  function Child() {
    const { slug, missing } = useOrganization()
    return (
      <p>
        {slug}:{missing ?? "none"}
      </p>
    )
  }
  const gate = (workspace: string | null) => (
    <ClientProvider client={forgeClient}>
      <OrganizationGate workspace={workspace} fallback={() => <p>Gated</p>}>
        <Child />
      </OrganizationGate>
    </ClientProvider>
  )
  const { rerender } = render(gate("acme"))
  await screen.findByText("acme:none")
  rerender(gate("bolt"))
  await screen.findByText("bolt:none")
  expect(switched).toEqual(["B"])
  rerender(gate("nobody"))
  await screen.findByText("bolt:nobody")
  rerender(gate(null))
  await screen.findByText("bolt:none")
  expect(switched).toEqual(["B"])
})

it("points links into the new workspace before a switch the person asked for renders", async () => {
  let org = "A"
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (path.endsWith("/session"))
        return Response.json({ user_id: "user", org_id: org, permissions: [] })
      if (path.endsWith("/orgs"))
        return Response.json({
          organizations: [
            { id: "A", name: "Acme", slug: "acme" },
            { id: "B", name: "Bolt", slug: "bolt" },
          ],
        })
      if (path.endsWith("/switch-org")) {
        org = JSON.parse(String(init?.body)).org_id
        return Response.json({})
      }
      throw new Error(path)
    }
  )
  let workspace = "acme"
  const onSwitch = vi.fn(() => {
    // App navigates home here; the address follows the new workspace.
    workspace = "bolt"
    rerender(gate())
  })
  function Child() {
    const { slug, switchOrganization } = useOrganization()
    return <button onClick={() => void switchOrganization("B")}>{slug}</button>
  }
  const gate = () => (
    <ClientProvider client={forgeClient}>
      <OrganizationGate
        workspace={workspace}
        onSwitch={onSwitch}
        fallback={() => <p>Gated</p>}
      >
        <Child />
      </OrganizationGate>
    </ClientProvider>
  )
  const { rerender } = render(gate())
  await userEvent
    .setup()
    .click(await screen.findByRole("button", { name: "acme" }))
  await screen.findByRole("button", { name: "bolt" })
  expect(onSwitch).toHaveBeenCalledOnce()
  expect(org).toBe("B")
})
