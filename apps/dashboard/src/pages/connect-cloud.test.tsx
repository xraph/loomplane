import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { ConnectCloudPage } from "./connect-cloud"

const navigate = vi.fn()

vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  Link: ({
    children,
    to,
    params,
    ...rest
  }: React.ComponentProps<"a"> & {
    to: string
    params?: Record<string, string>
  }) => (
    <a
      href={Object.entries(params ?? {}).reduce(
        (path, [k, v]) => path.replace(`$${k}`, v),
        to
      )}
      {...rest}
    >
      {children}
    </a>
  ),
}))

vi.mock("@/components/site-header", () => ({ SiteHeader: () => null }))

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
  navigate.mockReset()
})

type Posted = { path: string; body: Record<string, unknown> }

function stub(methods: string[], posted: Posted[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST") {
        posted.push({
          path,
          body: JSON.parse(String(init.body ?? "{}")) as Record<
            string,
            unknown
          >,
        })
        return Response.json(
          { id: "conn_1", name: "Local registry", status: "ready" },
          { status: 201 }
        )
      }
      if (path.includes("connection-clouds"))
        return Response.json({
          clouds: [
            { cloud: "registry", title: "Image registry", methods },
            { cloud: "kubernetes", title: "Kubernetes", methods },
            { cloud: "aws", title: "AWS", builtin: true, methods: ["token"] },
            {
              cloud: "cloudflare",
              title: "Cloudflare",
              builtin: false,
              credential: "API token",
              help: "Scope it with Zone Read and DNS Edit.",
              settings: [
                { key: "account_id", label: "Account ID", required: true },
              ],
              methods: ["token"],
            },
          ],
        })
      return Response.json({ connections: [] })
    })
  )
}

function page(cloud: string) {
  render(
    <ClientProvider client={forgeClient}>
      <ConnectCloudPage cloud={cloud} />
    </ClientProvider>
  )
}

it("connects a local registry from its preset in one click", async () => {
  const posted: Posted[] = []
  stub(["token"], posted)
  page("registry")
  const user = userEvent.setup()
  await user.click(screen.getByRole("radio", { name: /Local registry/ }))
  expect(screen.getByLabelText("Endpoint")).toHaveProperty(
    "value",
    "http://localhost:5000"
  )
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0].body).toEqual({
    cloud: "registry",
    name: "Local registry",
    credential: "{}",
    settings: { endpoint: "http://localhost:5000", repository: "loomplane" },
  })
  expect(navigate).toHaveBeenCalledWith({
    to: "/clouds/accounts/$id",
    params: { id: "conn_1" },
  })
})

it("hides the managed registry until it's turned on", async () => {
  stub(["token"], [])
  page("kubernetes")
  expect(screen.queryByLabelText("Registry hostname")).toBeNull()
  const user = userEvent.setup()
  await user.click(screen.getByRole("radio", { name: /Bare-metal server/ }))
  expect(screen.getByLabelText("Registry hostname")).toBeTruthy()
  expect(screen.getByRole("radio", { name: "Local disk" })).toHaveProperty(
    "ariaChecked",
    "true"
  )
})

it("warns about a kubeconfig the API would refuse", async () => {
  stub(["token"], [])
  page("kubernetes")
  const user = userEvent.setup()
  await user.click(screen.getByLabelText("Kubeconfig"))
  await user.paste(
    "users:\n- name: x\n  user:\n    exec:\n      command: aws\n"
  )
  expect(screen.getByText("The API will refuse this kubeconfig")).toBeTruthy()
})

it("asks only for a namespace in the cluster Loomplane runs in", async () => {
  stub(["in_cluster", "token"], [])
  page("kubernetes")
  expect(await screen.findByText("The cluster Loomplane runs in")).toBeTruthy()
  expect(screen.queryByLabelText("Kubeconfig")).toBeNull()
})

it("switches to any other integration from the heading", async () => {
  stub(["token"], [])
  page("registry")
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole("button", { name: "Connect something else" })
  )
  expect(await screen.findByText("Servers")).toBeTruthy()
  await user.click(screen.getByRole("menuitem", { name: /Cloudflare/ }))
  expect(navigate).toHaveBeenCalledWith({
    to: "/integrations/connect/$cloud",
    params: { cloud: "cloudflare" },
  })
})

it("connects a managed cloud on the page, with its help beside the form", async () => {
  const posted: Posted[] = []
  stub(["token"], posted)
  page("cloudflare")
  const user = userEvent.setup()
  await user.type(await screen.findByLabelText("Name"), "DNS")
  await user.type(screen.getByLabelText("API token"), "tok")
  await user.type(screen.getByLabelText("Account ID"), "acc")
  expect(screen.getByText(/Zone Read and DNS Edit/)).toBeTruthy()
  // The page has its own switcher, so the dialog's cloud picker is gone.
  expect(screen.queryByText("Cloud")).toBeNull()
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0].body).toEqual({
    cloud: "cloudflare",
    name: "DNS",
    credential: "tok",
    settings: { account_id: "acc" },
  })
  expect(navigate).toHaveBeenCalledWith({
    to: "/clouds/accounts/$id",
    params: { id: "conn_1" },
  })
})

it("says so for an integration Loomplane doesn't offer", async () => {
  stub(["token"], [])
  page("nowhere")
  expect(
    await screen.findByText("Loomplane can't connect to nowhere")
  ).toBeTruthy()
})

it("picks a common AWS region in one click", async () => {
  stub(["token"], [])
  page("aws")
  const user = userEvent.setup()
  const region = await screen.findByLabelText("Region")
  expect(region).toHaveProperty("value", "us-east-1")
  await user.click(screen.getByRole("button", { name: "eu-west-1" }))
  expect(region).toHaveProperty("value", "eu-west-1")
  expect(screen.getByRole("button", { name: "eu-west-1" })).toHaveProperty(
    "ariaPressed",
    "true"
  )
})
