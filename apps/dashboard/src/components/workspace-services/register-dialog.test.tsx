import { ClientProvider } from "@forge-go/client-react"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import { forgeClient } from "@/client/runtime"
import { RegisterServiceDialog } from "@/components/workspace-services/register-dialog"

const nav = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/lib/router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => nav.navigate,
}))
vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
  nav.navigate.mockReset()
})

const DSN = "postgres://app:s3cret@db.example.com:5432/orders?sslmode=require"

type Post = { path: string; body: Record<string, unknown> }

function serve(
  check: Record<string, unknown> = {
    ok: true,
    version: "16.4",
    capabilities: { create_database: true, create_user: false },
  }
) {
  const posts: Post[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as Record<string, unknown>
        posts.push({ path, body })
        if (path.endsWith("/catalog/endpoints/probes"))
          return Response.json(check)
        if (path.endsWith("/catalog/endpoints/registrations"))
          return Response.json(
            {
              id: "endpoint_new",
              org_id: "org",
              name: body.name,
              engine: "postgres",
              host: "db.example.com",
              port: 5432,
              tls: "require",
              created_by: "u",
              created_at: "2026-11-23T00:00:00Z",
              revision: 1,
              origin: "registered",
              description: "",
              environment_label: "production",
              restricted: body.restricted,
              projects: body.projects,
              admin_set: false,
            },
            { status: 201 }
          )
      }
      if (path.endsWith("/connections"))
        return Response.json({
          connections: [
            {
              id: "conn_box",
              name: "hetzner-box",
              cloud: "server",
              status: "ready",
              details: { architecture: "amd64" },
              account: "",
              created_at: "",
              created_by: "",
              generation: 1,
              message: "",
              method: "token",
              missing: [],
              org_id: "org",
            },
          ],
        })
      if (path.endsWith("/projects"))
        return Response.json({
          projects: [
            { id: "project_shop", name: "shop", kind: "app" },
            { id: "project_billing", name: "billing", kind: "app" },
          ],
        })
      return Response.json({})
    }
  )
  return posts
}

function open() {
  const onOpenChange = vi.fn()
  render(
    <ClientProvider client={forgeClient}>
      <RegisterServiceDialog open onOpenChange={onOpenChange} />
    </ClientProvider>
  )
  return onOpenChange
}

test("a connection string is read, checked, then registered for the projects picked", async () => {
  const posts = serve()
  open()
  const user = userEvent.setup()
  const input = screen.getByLabelText("Connection string")
  expect(input).toHaveAttribute("type", "password")
  await user.type(input, DSN)
  // What it reads as, without the password.
  const reads = screen.getByRole("group", { name: "Reads as" })
  expect(reads).toHaveTextContent("PostgreSQL")
  expect(within(reads).getByText("db.example.com:5432")).toBeInTheDocument()
  expect(within(reads).getByText("orders")).toBeInTheDocument()
  expect(document.body).not.toHaveTextContent("s3cret")

  await user.click(screen.getByRole("button", { name: "Continue" }))
  const checkButton = await screen.findByRole("button", {
    name: "Check connection",
  })
  // Nothing goes on until it checks out.
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  await waitFor(() => expect(checkButton).toBeEnabled())
  await user.click(checkButton)
  expect(await screen.findByText("It checks out.")).toBeInTheDocument()
  expect(
    screen.getByText("These credentials can create databases, but not users.")
  ).toBeInTheDocument()
  expect(posts[0]).toEqual({
    path: "/api/v1/catalog/endpoints/probes",
    body: { engine: "postgres", dsn: DSN, target: "conn_box" },
  })

  await user.click(screen.getByRole("button", { name: "Continue" }))
  // The name comes from the database.
  expect(screen.getByLabelText("Name")).toHaveValue("orders")
  await user.click(screen.getByRole("radio", { name: /Only some projects/ }))
  const register = screen.getByRole("button", { name: "Register orders" })
  expect(register).toBeDisabled()
  await user.click(await screen.findByRole("checkbox", { name: "shop" }))
  await user.click(register)

  await waitFor(() =>
    expect(nav.navigate).toHaveBeenCalledWith({
      to: "/services/registered/$id",
      params: { id: "endpoint_new" },
    })
  )
  expect(posts[1]).toEqual({
    path: "/api/v1/catalog/endpoints/registrations",
    body: {
      name: "orders",
      description: "",
      environment_label: "production",
      engine: "postgres",
      dsn: DSN,
      target: "conn_box",
      restricted: true,
      projects: ["project_shop"],
    },
  })
})

test("a check that fails says why and goes no further", async () => {
  serve({
    ok: false,
    version: "16.4",
    problems: ["db.example.com:5432 refused the password for app"],
  })
  open()
  const user = userEvent.setup()
  await user.type(screen.getByLabelText("Connection string"), DSN)
  await user.click(screen.getByRole("button", { name: "Continue" }))
  const checkButton = await screen.findByRole("button", {
    name: "Check connection",
  })
  await waitFor(() => expect(checkButton).toBeEnabled())
  await user.click(checkButton)
  expect(
    await screen.findByText("db.example.com:5432 refused the password for app")
  ).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
})

test("pasting .env lines offers the connection strings in them", async () => {
  serve()
  open()
  const input = screen.getByLabelText("Connection string")
  fireEvent.paste(input, {
    clipboardData: {
      getData: () =>
        `DATABASE_URL=${DSN}\nREDIS_URL=redis://:pw@cache:6379/0\nPORT=8080`,
    },
  })
  const offer = await screen.findByText(
    "That has 2 connection strings. Which one?"
  )
  const choices = within(offer.parentElement!).getAllByRole("button")
  expect(choices.map((b) => b.textContent)).toEqual([
    "DATABASE_URLpostgres://app:••••••@db.example.com:5432/orders?sslmode=require",
    "REDIS_URLredis://:••••••@cache:6379/0",
  ])
  await userEvent.click(choices[1])
  expect(input).toHaveValue("redis://:pw@cache:6379/0")
  expect(screen.getByRole("group", { name: "Reads as" })).toHaveTextContent(
    "Redis"
  )
})

test("a missing sslmode is one click to fix", async () => {
  serve()
  open()
  const user = userEvent.setup()
  const input = screen.getByLabelText("Connection string")
  await user.type(input, "postgres://app:pw@db.example.com/orders")
  await user.click(screen.getByRole("button", { name: "Add sslmode=require" }))
  expect(input).toHaveValue(
    "postgres://app:pw@db.example.com/orders?sslmode=require"
  )
})

test("closing after typing a connection string asks first", async () => {
  serve()
  const onOpenChange = open()
  const user = userEvent.setup()
  await user.type(screen.getByLabelText("Connection string"), DSN)
  await user.keyboard("{Escape}")
  expect(
    await screen.findByText("Discard this connection string?")
  ).toBeInTheDocument()
  expect(onOpenChange).not.toHaveBeenCalled()
  await user.click(screen.getByRole("button", { name: "Discard" }))
  expect(onOpenChange).toHaveBeenCalledWith(false)
})
