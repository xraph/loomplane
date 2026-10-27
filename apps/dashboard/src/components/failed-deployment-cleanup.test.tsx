import { ClientProvider } from "@forge-go/client-react"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { FailedDeploymentCleanup } from "./failed-deployment-cleanup"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

function fixture(stale = false, denied = false) {
  let accepted = false
  const writes: { url: string; body: unknown }[] = []
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      let status = 200
      let body: unknown = {
        deployment_id: "dep",
        revision: "review-1",
        attempt: 1,
        status: "pending",
      }
      if (init?.method === "POST") {
        writes.push({
          url,
          body: init.body ? JSON.parse(String(init.body)) : null,
        })
        if (url.endsWith("/preview"))
          body = {
            deployment_id: "dep",
            revision: "review-1",
            resource: "database",
            provider: "cloud-sql",
            region: "us-central1",
            connection_id: "gcp-1",
            effect:
              "Delete the unpublished instance and its owned endpoint. Original retries stop.",
          }
        if (url.endsWith("/apply")) {
          if (stale) {
            status = 409
            body = { message: "Review changed" }
          } else accepted = true
        }
      } else if (denied) {
        status = 403
        body = { message: "Access denied" }
      } else if (!accepted) {
        status = 404
        body = { message: "Not found" }
      }
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      })
    }
  )
  return writes
}

it("requires review before submitting its exact revision and shows durable cleanup progress", async () => {
  const writes = fixture()
  const changed = vi.fn(async () => {})
  const user = userEvent.setup()
  render(
    <ClientProvider client={forgeClient}>
      <FailedDeploymentCleanup id="dep" canApprove onChanged={changed} />
    </ClientProvider>
  )
  await user.click(
    await screen.findByRole("button", { name: "Review cleanup" })
  )
  expect(await screen.findByRole("dialog")).toHaveTextContent(
    "Original retries stop"
  )
  expect(writes).toHaveLength(1)
  await user.click(screen.getByRole("button", { name: "Abandon and clean up" }))
  await screen.findByText(
    "Cleanup is running. Deployment retries are disabled."
  )
  expect(writes[1]).toMatchObject({ body: { revision: "review-1" } })
  await waitFor(() => expect(changed).toHaveBeenCalledOnce())
  expect(screen.queryByRole("button", { name: "Review cleanup" })).toBeNull()
})

it("discards a stale review and requires a new preview", async () => {
  const writes = fixture(true)
  const user = userEvent.setup()
  render(
    <ClientProvider client={forgeClient}>
      <FailedDeploymentCleanup id="dep" canApprove onChanged={async () => {}} />
    </ClientProvider>
  )
  await user.click(
    await screen.findByRole("button", { name: "Review cleanup" })
  )
  await user.click(
    await screen.findByRole("button", { name: "Abandon and clean up" })
  )
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(screen.getByRole("alert")).toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "Review cleanup" }))
  await screen.findByRole("dialog")
  expect(writes.filter((w) => w.url.endsWith("/preview"))).toHaveLength(2)
})

it("does not treat denied status as an absent cleanup", async () => {
  fixture(false, true)
  render(
    <ClientProvider client={forgeClient}>
      <FailedDeploymentCleanup id="dep" canApprove onChanged={async () => {}} />
    </ClientProvider>
  )
  await screen.findByRole("alert")
  expect(screen.queryByRole("button", { name: "Review cleanup" })).toBeNull()
})
