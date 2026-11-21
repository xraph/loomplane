import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"
import type { Plan } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { PlanVariableMapping, planOutputOptions } from "./plan-variable-mapping"

vi.mock("@/lib/organization", () => ({
  useOrganization: () => ({ can: () => true }),
}))
vi.mock("@/lib/project-permissions", () => ({
  useProjectCan: () => () => true,
}))

const plan = {
  projectId: "proj",
  environment: "production",
  actions: [
    {
      name: "pg",
      resource: {
        existingResource: { resourceId: "providerresource_" + "a".repeat(64) },
      },
    },
    {
      name: "api",
      service: {
        service: "service-api",
        env: [
          {
            key: "DATABASE_URL",
            subject: "resource",
            from: "pg",
            field: "url",
          },
          {
            key: "FABRIQ_URL",
            subject: "resource",
            from: "fabriq",
            field: "consumer:url",
          },
        ],
      },
    },
    {
      name: "worker",
      service: {
        service: "service-worker",
        env: [{ key: "CACHE", from: "cache", subject: "resource" }],
      },
    },
  ],
} as unknown as Plan

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

test("the workspace can identify a shared attachment across project names", () => {
  expect(planOutputOptions(plan, "workspace")).toContainEqual({
    value: "providerresource_" + "a".repeat(64),
    label: "pg · connection string",
  })
  expect(planOutputOptions(plan, "service:service-worker")).toEqual([
    { value: "cache", label: "cache · connection string" },
  ])
})

test("a plan saves a service override by reference before replanning", async () => {
  const calls: { path: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({
        path: new URL(String(input), "http://localhost").pathname,
        body: JSON.parse(String(init?.body)),
      })
      return new Response(
        JSON.stringify({
          id: "var_1",
          key: "APP_DB_URL",
          value: "",
          scope: "service",
          org_id: "org",
          created_at: "2026-11-01",
          created_by: "user",
          updated_at: "2026-11-01",
          updated_by: "user",
          secret: false,
          environment: "production",
          resource_output: "pg",
        }),
        { headers: { "Content-Type": "application/json" } }
      )
    }
  )
  const replan = vi.fn(async () => {
    expect(calls).toHaveLength(1)
  })
  render(
    <ClientProvider client={forgeClient}>
      <PlanVariableMapping plan={plan} replan={replan} />
    </ClientProvider>
  )
  await userEvent.click(screen.getByRole("button", { name: "Map variable" }))
  await userEvent.click(screen.getByRole("combobox", { name: "Scope" }))
  await userEvent.click(screen.getByRole("option", { name: "Service · api" }))
  await userEvent.type(screen.getByLabelText("Key"), "APP_DB_URL")
  await userEvent.click(
    screen.getByRole("combobox", { name: "Choose a resource output" })
  )
  await userEvent.click(
    screen.getByRole("option", { name: "pg · connection string" })
  )
  await userEvent.click(
    screen.getByRole("button", { name: "Save mapping and plan again" })
  )
  await vi.waitFor(() => expect(replan).toHaveBeenCalledOnce())
  expect(calls).toEqual([
    {
      path: "/api/v1/projects/proj/variables",
      body: {
        key: "APP_DB_URL",
        service: "service-api",
        environment: "production",
        resource_output: "pg",
        value: "",
      },
    },
  ])
})

test("a blocked plan can save a database mapping before the service is reachable", () => {
  const blocked = {
    actions: [
      { op: "create", name: "pg", resource: { kind: "postgres" } },
      { op: "delete", name: "old", resource: { kind: "postgres" } },
    ],
  } as Plan
  expect(planOutputOptions(blocked, "project")).toEqual([
    { label: "pg · connection string", value: "pg" },
  ])
})
