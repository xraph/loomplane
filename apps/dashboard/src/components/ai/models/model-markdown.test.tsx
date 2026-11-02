import { render, screen } from "@testing-library/react"
import { expect, it } from "vitest"

import { ModelMarkdown } from "@/components/ai/models/model-markdown"

it("renders a model card without raw HTML", () => {
  const { container } = render(
    <ModelMarkdown
      markdown={[
        "# Qwen2.5",
        "",
        '<script>window.pwned = true</script><img src="x" onerror="alert(1)">',
        "",
        '<div class="evil">raw block</div>',
        "",
        "| GPU | Memory |",
        "| --- | ------ |",
        "| L40S | 48 GB |",
        "",
        "[Paper](https://arxiv.org/abs/2412.15115) and [bad](javascript:alert(1))",
        "",
        "![chart](https://example.com/chart.png)",
      ].join("\n")}
    />
  )
  // Raw HTML is dropped, never rendered.
  expect(container.querySelector("script")).toBeNull()
  expect(container.querySelector("img")).toBeNull()
  expect(container.querySelector(".evil")).toBeNull()
  expect(container.innerHTML).not.toContain("onerror")
  expect(container.textContent).not.toContain("window.pwned")
  // A GFM table renders as a table.
  const table = screen.getByRole("table")
  expect(table).toHaveTextContent("L40S")
  expect(screen.getByRole("columnheader", { name: "Memory" })).toBeVisible()
  // Links open apart from the page, with no referrer, and a script URL is
  // never a link target.
  const paper = screen.getByRole("link", { name: "Paper" })
  expect(paper).toHaveAttribute("target", "_blank")
  expect(paper).toHaveAttribute("rel", "noreferrer")
  expect(
    screen.getByText("bad").closest("a")?.getAttribute("href") ?? ""
  ).not.toContain("javascript")
  // An image shows as its alt text, so the card can't load it.
  expect(screen.getByText("[chart]")).toBeInTheDocument()
})
