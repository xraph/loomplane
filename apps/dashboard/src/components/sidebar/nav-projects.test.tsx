import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { NavProjects } from "./nav-projects"

afterEach(cleanup)

function renderNav(newDisabled?: string) {
  const onNew = vi.fn()
  render(
    <SidebarProvider>
      <NavProjects projects={[]} onNew={onNew} newDisabled={newDisabled} />
    </SidebarProvider>
  )
  return onNew
}

it("opens a new project from the entry", async () => {
  const onNew = renderNav()
  await userEvent.click(screen.getByRole("button", { name: /New project/ }))
  expect(onNew).toHaveBeenCalledOnce()
})

it("disables the entry with its reason while the workspace needs a card", () => {
  const onNew = renderNav("Add a card to create projects")
  const entry = screen.getByRole("button", { name: /New project/ })
  expect(entry).toHaveAttribute("aria-disabled", "true")
  expect(entry).toHaveAccessibleDescription("Add a card to create projects")
  entry.click()
  expect(onNew).not.toHaveBeenCalled()
})
