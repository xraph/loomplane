import { describe, expect, it } from "vitest"

import { parseDotenv } from "./dotenv"

describe("parseDotenv", () => {
  it("reads the forms dotenv files use", () => {
    const { entries, problems } = parseDotenv(
      [
        "﻿# a comment",
        "export DATABASE_URL=postgres://u:p@host/db",
        "LOG_LEVEL=info # or debug",
        "URL=https://example.com/#anchor",
        'GREETING="hello\\nworld"',
        "RAW='keep \\n as it is'",
        'KEY="-----BEGIN KEY-----',
        "abc",
        '-----END KEY-----"',
        "EMPTY=",
        "  SPACED  =  padded  ",
      ].join("\r\n")
    )
    expect(problems).toEqual([])
    expect(Object.fromEntries(entries.map((e) => [e.key, e.value]))).toEqual({
      DATABASE_URL: "postgres://u:p@host/db",
      LOG_LEVEL: "info",
      URL: "https://example.com/#anchor",
      GREETING: "hello\nworld",
      RAW: "keep \\n as it is",
      KEY: "-----BEGIN KEY-----\nabc\n-----END KEY-----",
      EMPTY: "",
      SPACED: "padded",
    })
    expect(entries.find((e) => e.key === "KEY")?.line).toBe(7)
  })

  it("keeps the last of a key set twice, and says so", () => {
    const { entries, problems } = parseDotenv("A=1\nB=2\nA=3")
    expect(entries.map((e) => `${e.key}=${e.value}`)).toEqual(["B=2", "A=3"])
    expect(problems).toEqual([
      { line: 3, message: "A is set more than once; the last value is kept." },
    ])
  })

  it("leaves out what it can't set, line by line", () => {
    const { entries, problems } = parseDotenv(
      'just words\n1BAD=x\nOPEN="no end'
    )
    expect(problems.map((p) => p.line)).toEqual([1, 2, 3])
    expect(entries).toEqual([{ key: "OPEN", value: "no end", line: 3 }])
  })
})
