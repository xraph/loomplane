import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, test } from "vitest"
import { CloudGlyph, cloudProduct } from "@/components/cloud-glyph"
afterEach(cleanup)
test("Azure uses its official product mark and shared label", () => {
  expect(cloudProduct("azure")).toBe("azure")
  const { container } = render(<CloudGlyph cloud="azure" />)
  expect(screen.getByTitle("Azure")).toBeInTheDocument()
  expect(container.querySelector("img")).toHaveAttribute(
    "src",
    "/logos/azure.svg"
  )
})
