import { act, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it, vi } from "vitest"

import { ThemeProvider } from "@/components/theme-provider"
import { TopBar } from "@/components/top-bar"
import { SidebarProvider } from "@/components/ui/sidebar"
import { applyAccent, resolveAccent } from "@/lib/accent"

function Header() {
  return (
    <ThemeProvider>
      <SidebarProvider>
        <TopBar crumbs={[{ label: "Fabriq" }]} onSearch={() => {}} />
      </SidebarProvider>
    </ThemeProvider>
  )
}

afterEach(() => {
  vi.restoreAllMocks()
  delete document.documentElement.dataset.accent
})

it("offers every mock accent and restores the selected color after reload", async () => {
  applyAccent()
  const first = render(<Header />)
  const colors = within(
    screen.getByRole("radiogroup", { name: "Primary color" })
  )
  expect(colors.getAllByRole("radio")).toHaveLength(5)
  await userEvent.click(
    colors.getByRole("radio", { name: "Copper primary color" })
  )
  expect(document.documentElement).toHaveAttribute("data-accent", "copper")
  expect(localStorage.getItem("lp-accent")).toBe("copper")
  first.unmount()
  delete document.documentElement.dataset.accent
  applyAccent()
  render(<Header />)
  expect(
    screen.getByRole("radio", { name: "Copper primary color" })
  ).toHaveAttribute("aria-checked", "true")
  expect(document.documentElement).toHaveAttribute("data-accent", "copper")
})

it("supports arrow-key selection and synchronizes preferences from another tab", async () => {
  applyAccent("graphite")
  render(<Header />)
  const graphite = screen.getByRole("radio", { name: "Graphite primary color" })
  graphite.focus()
  await userEvent.keyboard("{ArrowRight}")
  expect(
    screen.getByRole("radio", { name: "Copper primary color" })
  ).toHaveFocus()
  expect(document.documentElement).toHaveAttribute("data-accent", "copper")
  act(() => {
    localStorage.setItem("lp-accent", "sage")
    window.dispatchEvent(
      new StorageEvent("storage", {
        key: "lp-accent",
        newValue: "sage",
        storageArea: localStorage,
      })
    )
  })
  expect(
    screen.getByRole("radio", { name: "Sage primary color" })
  ).toHaveAttribute("aria-checked", "true")
  expect(document.documentElement).toHaveAttribute("data-accent", "sage")
  act(() => {
    localStorage.removeItem("lp-accent")
    window.dispatchEvent(
      new StorageEvent("storage", { key: null, storageArea: localStorage })
    )
  })
  expect(document.documentElement).toHaveAttribute(
    "data-accent",
    resolveAccent()
  )
})

it("keeps color selection usable when preference storage is unavailable", async () => {
  applyAccent()
  render(<Header />)
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("storage denied")
  })
  await userEvent.click(
    screen.getByRole("radio", { name: "Plum primary color" })
  )
  expect(document.documentElement).toHaveAttribute("data-accent", "plum")
  expect(
    screen.getByRole("radio", { name: "Plum primary color" })
  ).toHaveAttribute("aria-checked", "true")
})
