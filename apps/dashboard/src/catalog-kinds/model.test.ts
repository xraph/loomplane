// A .ts file: the catalog discovers every .tsx file here as a kind.
import { ClientProvider } from "@forge-go/client-react"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
import { createElement } from "react"
import { expect, it } from "vitest"

import { forgeClient } from "@/client/runtime"
import {
  catalogKind,
  groupIcons,
  inCatalog,
  type CatalogPanelProps,
} from "@/lib/catalog-kinds"

const kinds = [
  "model-vllm",
  "model-tei",
  "model-llamacpp",
  "model-llamacpp-gpu",
]

it("links model service pages to the model page", async () => {
  for (const kind of kinds) {
    const module = catalogKind(kind)
    expect(module?.group).toBe("ai")
    expect(module?.hidden).toBe(true)
    expect(inCatalog({ name: kind, placement: "standalone" })).toBe(false)
    expect(groupIcons.ai).toBeDefined()
    const Actions = module!.actions!
    const props = {
      projectId: "proj_1",
      environmentId: "env_stg",
      resource: "qwen2-5-7b-instruct",
      kind,
    } as CatalogPanelProps
    const router = createRouter({
      routeTree: createRootRoute({
        component: () => createElement(Actions, props),
      }),
      history: createMemoryHistory({ initialEntries: ["/"] }),
    })
    const view = render(
      createElement(
        ClientProvider,
        { client: forgeClient },
        createElement(RouterProvider, { router })
      )
    )
    expect(
      await screen.findByRole("link", { name: "Open the model page" })
    ).toHaveAttribute(
      "href",
      "/projects/proj_1/ai/models/qwen2-5-7b-instruct?environment=env_stg"
    )
    view.unmount()
  }
})
