import { useState } from "react"
import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, expect, it } from "vitest"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { CloudSelect } from "./cloud-select"
afterEach(cleanup)
it("shows empty, initially selected and changed labels without exposing option IDs", async () => {
  const show = (value: string, loaded: boolean) => (
    <CloudSelect value={value}>
      <SelectTrigger aria-label="Pool">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="">Choose a pool</SelectItem>
        {loaded &&
          [
            { id: "pool-1", name: "Production" },
            { id: "pool-2", name: "Standby" },
          ].map((pool) => (
            <SelectItem key={pool.id} value={pool.id}>
              {pool.name}
            </SelectItem>
          ))}
      </SelectContent>
    </CloudSelect>
  )
  const view = render(show("", false))
  expect(screen.getByRole("combobox", { name: "Pool" })).toHaveTextContent(
    "Choose a pool"
  )
  view.rerender(show("pool-1", false))
  expect(screen.getByRole("combobox", { name: "Pool" })).toHaveTextContent(
    "Saved selection unavailable"
  )
  view.rerender(show("pool-1", true))
  expect(screen.getByRole("combobox", { name: "Pool" })).toHaveTextContent(
    "Production"
  )
})
it("shows the selected label after a portal option changes its value", async () => {
  const user = userEvent.setup()
  function Picker() {
    const [value, setValue] = useState("pool-1")
    return (
      <CloudSelect value={value} onValueChange={(next) => setValue(next ?? "")}>
        <SelectTrigger aria-label="Pool">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="pool-1">Production</SelectItem>
          <SelectItem value="pool-2">Standby</SelectItem>
        </SelectContent>
      </CloudSelect>
    )
  }
  render(<Picker />)
  await user.click(screen.getByRole("combobox", { name: "Pool" }))
  await user.click(await screen.findByRole("option", { name: "Standby" }))
  expect(screen.getByRole("combobox", { name: "Pool" })).toHaveTextContent(
    "Standby"
  )
})
