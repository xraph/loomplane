import { cleanup, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { afterEach, expect, it } from "vitest"

import { TagInput, tagProblem } from "./tag-input"

afterEach(cleanup)

function Harness({
  initial = [],
  suggestions = [],
}: {
  initial?: string[]
  suggestions?: string[]
}) {
  const [tags, setTags] = useState(initial)
  return (
    <>
      <TagInput tags={tags} onChange={setTags} suggestions={suggestions} />
      <output aria-label="Saved tags">{tags.join(",")}</output>
    </>
  )
}

const saved = () => screen.getByRole("status", { name: "Saved tags" })

it("adds tags on Enter and comma, as sorted lowercase slugs", async () => {
  const user = userEvent.setup()
  render(<Harness />)
  const input = screen.getByRole("textbox", { name: "Target tags" })
  await user.type(input, "Front End{Enter}")
  await user.type(input, "gpu, EU,")
  expect(saved()).toHaveTextContent("eu,front-end,gpu")
  expect(input).toHaveValue("")
})

it("explains a tag the API would refuse and keeps it out", async () => {
  const user = userEvent.setup()
  render(<Harness initial={["eu"]} />)
  const input = screen.getByRole("textbox", { name: "Target tags" })
  await user.type(input, "9lives")
  expect(screen.getByRole("alert")).toHaveTextContent(/starting with a letter/)
  await user.keyboard("{Enter}")
  expect(saved()).toHaveTextContent(/^eu$/)
  await user.clear(input)
  await user.type(input, "eu")
  expect(screen.getByRole("alert")).toHaveTextContent(/already on this target/)
})

it("removes the last tag with Backspace and offers the Cloud's other tags", async () => {
  const user = userEvent.setup()
  render(<Harness initial={["eu", "gpu"]} suggestions={["eu", "large"]} />)
  await user.click(screen.getByRole("textbox", { name: "Target tags" }))
  await user.keyboard("{Backspace}")
  expect(saved()).toHaveTextContent(/^eu$/)
  // eu is already on the target, so only large is offered.
  expect(screen.queryByRole("button", { name: "+ eu" })).not.toBeInTheDocument()
  await user.click(screen.getByRole("button", { name: "+ large" }))
  expect(saved()).toHaveTextContent("eu,large")
  await user.click(screen.getByRole("button", { name: "Remove tag eu" }))
  expect(saved()).toHaveTextContent(/^large$/)
})

it("caps a target at sixteen tags", () => {
  const sixteen = Array.from({ length: 16 }, (_, i) => `t${i}`)
  expect(tagProblem("another", sixteen)).toMatch(/at most 16/)
  expect(tagProblem("a".repeat(33), [])).toMatch(/32 characters/)
  expect(tagProblem("ok-tag", [])).toBe("")
})
