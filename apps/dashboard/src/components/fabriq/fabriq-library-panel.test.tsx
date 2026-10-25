import { screen } from "@testing-library/react"
import { afterEach, expect, test, vi } from "vitest"
import { forgeClient } from "@/client/runtime"
import { catalogKind } from "@/lib/catalog-kinds"
import { FabriqLibraryPanel } from "./fabriq-library-panel"
import { mount, panelProps, serve } from "./test-api"

afterEach(() => {
  vi.unstubAllGlobals()
  forgeClient.clear()
})

test("library panel lists conditional credentials and the stock worker limitation", () => {
  serve({
    status: {
      kind: "fabriq-library",
      reachable: true,
      version: "1.9",
      migrations: 0,
      migrations_known: false,
      entity_types: 0,
      keys_report_use: false,
    },
  })
  const props = panelProps({ kind: "fabriq-library" })
  props.config.composition!.dependencies.falkordb = {
    mode: "new",
    resource: "fabriq--falkordb",
  }
  mount(<FabriqLibraryPanel {...props} />)
  for (const key of [
    "FABRIQ_POSTGRES_DSN",
    "FABRIQ_REDIS_ADDR",
    "FABRIQ_REDIS_USERNAME",
    "FABRIQ_REDIS_PASSWORD",
    "FABRIQ_FALKORDB_ADDR",
    "FABRIQ_FALKORDB_USERNAME",
    "FABRIQ_FALKORDB_PASSWORD",
  ])
    expect(screen.getByText(key)).toBeVisible()
  expect(screen.queryByText("FABRIQ_ELASTICSEARCH_ADDRS")).toBeNull()
  expect(screen.queryByText("FABRIQ_URL")).toBeNull()
  expect(screen.getByText(/forgeext.WithWorker\(true\)/)).toBeVisible()
  expect(screen.getByText("Fabriq worker")).toBeVisible()
  expect(screen.queryByText("Admin console")).toBeNull()
  const module = catalogKind("fabriq-library")
  expect(module?.hidden).toBe(true)
  expect(module?.panels.map((p) => p.id)).toEqual([
    "library",
    "engines",
    "recovery",
    "settings",
  ])
  expect(module?.actions).toBeUndefined()
})

test("library explains conditional credentials without exposing their values", () => {
  serve()
  mount(<FabriqLibraryPanel {...panelProps({ kind: "fabriq-library" })} />)
  expect(
    screen.getByText(
      "FalkorDB and Elasticsearch credentials are included when you enable those engines."
    )
  ).toBeVisible()
  expect(screen.queryByText("FABRIQ_FALKORDB_PASSWORD")).toBeNull()
})
