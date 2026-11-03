import { describe, expect, it } from "vitest"

import type { Graph, Service } from "@/client/forge/loomplane/types"

import {
  detected,
  rowsFromDetected,
  rowsFromPaste,
  whereRead,
} from "./env-import"

const service = (id: string, deployable: boolean, env: Service["env"]) =>
  ({ id, name: id, deployable, env }) as Service

const ev = (path: string, line = 1) => ({ path, line, detail: "" })

describe("detected", () => {
  const services = [
    service("api", true, [
      {
        name: "DATABASE_URL",
        confidence: "certain",
        evidence: [ev("api/db.go")],
      },
      { name: "PORT", confidence: "certain", evidence: [ev("api/main.go")] },
    ]),
    service("web", true, [
      {
        name: "DATABASE_URL",
        confidence: "certain",
        example: "postgres://localhost/app",
        evidence: [ev("web/.env.example")],
      },
      {
        name: "NEXT_PUBLIC_URL",
        confidence: "certain",
        evidence: [ev("web/app.ts")],
      },
    ]),
    service("cli", false, [
      { name: "CLI_ONLY", confidence: "certain", evidence: [] },
    ]),
  ]
  const graph = { services, edges: [] } as unknown as Graph

  it("joins every deployable service's for the whole project", () => {
    const all = detected(graph, "")
    expect(all.map((v) => v.name)).toEqual(["DATABASE_URL", "NEXT_PUBLIC_URL"])
    expect(all[0].evidence).toHaveLength(2)
    expect(all[0].example).toBe("postgres://localhost/app")
  })

  it("keeps to one service, and leaves out what Loomplane sets", () => {
    expect(detected(graph, "api").map((v) => v.name)).toEqual(["DATABASE_URL"])
  })

  it("says when Loomplane sets a key for a resource the service needs", () => {
    const needs = {
      ...graph,
      edges: [{ from: "web", to: "res:postgres", kind: "needs" }],
    } as unknown as Graph
    const web = detected(needs, "web")
    expect(web.find((v) => v.name === "DATABASE_URL")?.providedBy).toBe(
      "postgres"
    )
    expect(detected(needs, "api")[0].providedBy).toBeUndefined()
    expect(detected(needs, "")[0].providedBy).toBe("postgres")
    expect(rowsFromDetected(web, new Set())[0]).toMatchObject({
      key: "DATABASE_URL",
      include: false,
      providedBy: "postgres",
    })
  })

  it("doesn't change the graph it reads", () => {
    detected(graph, "")
    expect(services[0].env?.[0].evidence).toHaveLength(1)
  })
})

describe("rows", () => {
  it("offer only what isn't set yet, with its example", () => {
    const rows = rowsFromDetected(
      [
        { name: "A", confidence: "certain", example: "x", evidence: [] },
        { name: "B", confidence: "certain", evidence: [] },
      ],
      new Set(["B"])
    )
    expect(rows).toMatchObject([{ key: "A", value: "x", include: true }])
  })

  it("come from pasted lines", () => {
    const { rows, problems } = rowsFromPaste("A=1\nnot a line")
    expect(rows).toMatchObject([{ key: "A", value: "1", include: true }])
    expect(problems).toHaveLength(1)
  })

  it("say where a variable is read", () => {
    expect(whereRead([ev("a.go", 3), ev("b.go")])).toBe("a.go:3 and 1 more")
    expect(whereRead([])).toBe("")
  })
})
