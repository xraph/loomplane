import { ClientProvider } from "@forge-go/client-react"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, test, vi } from "vitest"

import type { Project } from "@/client/forge/loomplane/types"
import { forgeClient } from "@/client/runtime"
import { ProjectGeneral, projectChange } from "@/components/project-general"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

const project = {
  id: "p",
  name: "Loomplane GitHub verification",
  slug: "loomplane-github-verification",
  description: "Private repository analysis.",
  icon: "shield",
  color: "orange",
} as Project

function serve() {
  const sent: unknown[] = []
  vi.stubGlobal("fetch", async (_: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body ?? "{}")) as Record<
      string,
      string
    >
    sent.push(body)
    return new Response(
      JSON.stringify({
        ...project,
        ...body,
        updated_at: "2026-10-27T18:00:00Z",
      }),
      { headers: { "Content-Type": "application/json" } }
    )
  })
  render(
    <ClientProvider client={forgeClient}>
      <ProjectGeneral project={project} mayEdit />
    </ClientProvider>
  )
  return sent
}

test("renaming sends only the name, trimmed", async () => {
  const sent = serve()
  const user = userEvent.setup()
  const save = screen.getByRole("button", { name: "Save changes" })
  expect(save).toBeDisabled()
  const name = screen.getByLabelText("Name")
  await user.clear(name)
  await user.type(name, "  GitHub verification  ")
  await user.click(save)
  expect(sent).toEqual([{ name: "GitHub verification" }])
  expect(
    await screen.findByDisplayValue("GitHub verification")
  ).toBeInTheDocument()
})

test("a project can't be saved without a name", async () => {
  serve()
  const user = userEvent.setup()
  await user.clear(screen.getByLabelText("Name"))
  expect(screen.getByText("A project needs a name.")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled()
})

test("reset puts the form back", async () => {
  serve()
  const user = userEvent.setup()
  await user.type(screen.getByLabelText("Description"), " More.")
  await user.click(screen.getByRole("button", { name: "Reset" }))
  expect(screen.getByLabelText("Description")).toHaveValue(
    "Private repository analysis."
  )
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled()
})

test("a viewer sees the details but can't change them", () => {
  render(
    <ClientProvider client={forgeClient}>
      <ProjectGeneral project={project} mayEdit={false} />
    </ClientProvider>
  )
  expect(screen.getByLabelText("Name")).toBeDisabled()
  expect(
    screen.queryByRole("button", { name: "Save changes" })
  ).not.toBeInTheDocument()
})

test("projectChange keeps only what differs", () => {
  expect(
    projectChange(project, {
      name: "Loomplane GitHub verification",
      description: " Private repository analysis. ",
      icon: "shield",
      color: "teal",
    })
  ).toEqual({ color: "teal" })
})
