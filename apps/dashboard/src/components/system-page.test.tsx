import { render, screen } from "@testing-library/react"
import { expect, test } from "vitest"

import { Problem } from "@/App"

test("the API not answering says where it looked and offers to try again", () => {
  render(
    <Problem
      title="Loomplane isn't answering"
      message="The dashboard couldn't reach Loomplane's API."
      address="localhost:5173/api"
    />
  )
  expect(screen.getByRole("alert")).toHaveTextContent(
    "Loomplane isn't answering"
  )
  expect(
    screen.getByRole("heading", { name: "Loomplane isn't answering" })
  ).toBeInTheDocument()
  expect(screen.getByText("localhost:5173/api")).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument()
})

test("a problem retrying can't fix has no Try again", () => {
  render(
    <Problem
      title="This invitation link is incomplete"
      message="Open the full link from your invitation email."
      retry={false}
    />
  )
  expect(
    screen.queryByRole("button", { name: "Try again" })
  ).not.toBeInTheDocument()
})
