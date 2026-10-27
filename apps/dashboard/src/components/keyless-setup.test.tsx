import { ClientProvider } from "@forge-go/client-react"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { KeylessSetup } from "./keyless-setup"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const setup = {
  connection_id: "conn_k1",
  issuer: "https://loomplane.example",
  subject: "org:org_1:connection:conn_k1",
  audience: "sts.amazonaws.com",
  expires_at: "2026-10-27T10:00:00Z",
  steps: [
    {
      title: "Add the identity provider",
      body: "Create an OpenID Connect provider in IAM.",
      command:
        "aws iam create-open-id-connect-provider --url https://loomplane.example",
    },
    { title: "Make the role", body: "Trust the provider for this subject." },
  ],
}

type Posted = { path: string; method: string; body: Record<string, unknown> }

function stub(posted: Posted[] = [], answer: object = setup) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input instanceof Request ? input.url : input)
      if (init?.method === "POST" || init?.method === "PUT") {
        posted.push({
          path,
          method: init.method,
          body: JSON.parse(String(init.body ?? "{}")) as Record<
            string,
            unknown
          >,
        })
        if (path.endsWith("/keyless/prepare")) return Response.json(answer)
        return Response.json({ id: "conn_k1", name: "Prod", status: "ready" })
      }
      if (path.includes("/keyless")) return Response.json(answer)
      return Response.json({})
    })
  )
}

function panel(props: Partial<Parameters<typeof KeylessSetup>[0]> = {}) {
  render(
    <ClientProvider client={forgeClient}>
      <KeylessSetup
        cloud="aws"
        title="AWS"
        name="Prod"
        fields={[
          { key: "role_arn", label: "Role ARN", required: true },
          { key: "region", label: "Region" },
        ]}
        onConnected={vi.fn()}
        {...props}
      />
    </ClientProvider>
  )
}

it("shows what to paste into the cloud", async () => {
  stub()
  panel()
  expect(await screen.findByText("https://loomplane.example")).toBeTruthy()
  expect(screen.getByText("org:org_1:connection:conn_k1")).toBeTruthy()
  expect(screen.getByText("sts.amazonaws.com")).toBeTruthy()
  expect(screen.getByText("Add the identity provider")).toBeTruthy()
  expect(
    screen.getByText(/aws iam create-open-id-connect-provider/)
  ).toBeTruthy()
  expect(screen.getByText("Make the role")).toBeTruthy()
  expect(screen.getByText(/Reserved until/)).toBeTruthy()
  expect(screen.getByText(/you'll get a new subject/)).toBeTruthy()
  expect(screen.getByText(/Set up AWS to trust/)).toBeTruthy()
})

it("copies each value", async () => {
  stub()
  panel()
  const user = userEvent.setup()
  await screen.findByText("https://loomplane.example")
  const copied = async (label: string) => {
    await user.click(screen.getByRole("button", { name: label }))
    return navigator.clipboard.readText()
  }
  expect(await copied("Copy issuer")).toBe("https://loomplane.example")
  expect(await copied("Copy subject")).toBe("org:org_1:connection:conn_k1")
  expect(await copied("Copy audience")).toBe("sts.amazonaws.com")
  expect(await copied("Copy command: Add the identity provider")).toBe(
    setup.steps[0]!.command
  )
})

it("names each step's command in its copy button", async () => {
  const twoCommands = {
    ...setup,
    steps: [
      setup.steps[0]!,
      { title: "Make the role", command: "aws iam create-role" },
    ],
  }
  stub([], twoCommands)
  panel()
  await screen.findByText("https://loomplane.example")
  expect(
    screen.getByRole("button", {
      name: "Copy command: Add the identity provider",
    })
  ).toBeTruthy()
  expect(
    screen.getByRole("button", { name: "Copy command: Make the role" })
  ).toBeTruthy()
})

it("keeps and clears optional settings on a switch", async () => {
  const posted: Posted[] = []
  stub(posted)
  panel({
    name: "",
    fields: [
      { key: "role_arn", label: "Role ARN", required: true },
      { key: "certificate_arn", label: "Certificate ARN (optional)" },
      { key: "billing_export", label: "Billing export table (optional)" },
    ],
    replacing: {
      id: "conn_a",
      name: "Prod AWS",
      cloud: "aws",
      method: "token",
      status: "ready",
      message: "",
      account: "123456789012",
      details: {
        region: "eu-west-1",
        certificate_arn: "arn:aws:acm:eu-west-1:1:certificate/old",
        billing_export: "p.d.t",
      },
      missing: [],
      createdAt: "2026-10-01T00:00:00Z",
      createdBy: "u1",
    } as unknown as Parameters<typeof KeylessSetup>[0]["replacing"],
  })
  const user = userEvent.setup()
  await screen.findByText("https://loomplane.example")
  expect(
    (screen.getByLabelText("Certificate ARN (optional)") as HTMLInputElement)
      .value
  ).toBe("arn:aws:acm:eu-west-1:1:certificate/old")
  await user.type(screen.getByLabelText("Role ARN"), "arn:aws:iam::1:role/x")
  await user.clear(screen.getByLabelText("Billing export table (optional)"))
  await user.click(screen.getByRole("button", { name: "Check and switch" }))
  await waitFor(() => expect(posted).toHaveLength(1))
  expect(posted[0]!.body).toEqual({
    settings: {
      role_arn: "arn:aws:iam::1:role/x",
      certificate_arn: "arn:aws:acm:eu-west-1:1:certificate/old",
      billing_export: "",
    },
  })
})

it("fills Google's audience from the provider", async () => {
  stub([], { ...setup, audience: undefined })
  panel({
    cloud: "gcp",
    title: "Google Cloud",
    fields: [
      { key: "project_id", label: "Project ID", required: true },
      { key: "workload_provider", label: "Workload provider", required: true },
    ],
  })
  const user = userEvent.setup()
  await screen.findByText("https://loomplane.example")
  expect(screen.getByText("//iam.googleapis.com/")).toBeTruthy()
  expect(
    (screen.getByRole("button", { name: "Copy audience" }) as HTMLButtonElement)
      .disabled
  ).toBe(true)
  const provider =
    "projects/1/locations/global/workloadIdentityPools/p/providers/q"
  await user.type(screen.getByLabelText("Workload provider"), ` ${provider}`)
  expect(screen.getByText(`//iam.googleapis.com/${provider}`)).toBeTruthy()
  await user.click(screen.getByRole("button", { name: "Copy audience" }))
  expect(await navigator.clipboard.readText()).toBe(
    `//iam.googleapis.com/${provider}`
  )
})

it("connects with the reserved id", async () => {
  const posted: Posted[] = []
  stub(posted)
  const onConnected = vi.fn()
  panel({ onConnected })
  const user = userEvent.setup()
  await screen.findByText("https://loomplane.example")
  await user.type(screen.getByLabelText("Role ARN"), " arn:aws:iam::1:role/x ")
  await user.click(screen.getByRole("button", { name: "Check and connect" }))
  await waitFor(() => expect(onConnected).toHaveBeenCalled())
  expect(posted.map((p) => p.path.replace(/^.*\/api\/v1/, ""))).toEqual([
    "/connections/keyless/prepare",
    "/connections/keyless",
  ])
  expect(posted[0]!.body).toEqual({ cloud: "aws" })
  expect(posted[1]!.body).toEqual({
    connection_id: "conn_k1",
    name: "Prod",
    settings: { role_arn: "arn:aws:iam::1:role/x" },
  })
})

it("names the cloud in the intro", async () => {
  stub()
  panel({ cloud: "azure", title: "Azure" })
  expect(await screen.findByText(/Set up Azure to trust/)).toBeTruthy()
})

it("stops showing the tick once the value changes", async () => {
  stub([], { ...setup, audience: undefined })
  panel({
    cloud: "gcp",
    title: "Google Cloud",
    fields: [{ key: "workload_provider", label: "Workload provider" }],
  })
  const user = userEvent.setup()
  await screen.findByText("https://loomplane.example")
  await user.type(screen.getByLabelText("Workload provider"), "a")
  const copy = screen.getByRole("button", { name: "Copy audience" })
  await user.click(copy)
  expect(copy.querySelector(".lucide-check")).toBeTruthy()
  await user.type(screen.getByLabelText("Workload provider"), "b")
  expect(copy.querySelector(".lucide-check")).toBeNull()
})
