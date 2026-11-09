import { render, screen } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"

import {
  FeaturesProvider,
  requireFeature,
  setFeatures,
  useFeatures,
} from "@/lib/features"

afterEach(() => setFeatures({ ai: false }))

function Shows() {
  return <p>{useFeatures().ai ? "AI on" : "AI off"}</p>
}

it("leaves the route check to startup", () => {
  // Rendering the provider says what's on to components and nothing else,
  // so one test's provider can't switch a route on for the next.
  render(
    <FeaturesProvider features={{ ai: true }}>
      <Shows />
    </FeaturesProvider>
  )
  expect(screen.getByText("AI on")).toBeInTheDocument()
  expect(() => requireFeature("ai")).toThrow()

  setFeatures({ ai: true })
  expect(() => requireFeature("ai")).not.toThrow()
})

it("keeps everything off without a provider", () => {
  render(<Shows />)
  expect(screen.getByText("AI off")).toBeInTheDocument()
})
