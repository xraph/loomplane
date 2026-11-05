import { expect, it } from "vitest"

import {
  dollars,
  monthDay,
  parseAmount,
  parseRate,
  parseUSD,
  wireDollars,
} from "./money"

it("formats and parses dollars", () => {
  expect(dollars(300)).toBe("$3.00")
  expect(dollars(5)).toBe("$0.05")
  expect(dollars(123456)).toBe("$1,234.56")
  expect(wireDollars(2505)).toBe("25.05")
  expect(parseUSD("25")).toEqual({ cents: 2500 })
  expect(parseUSD("$25.5")).toEqual({ cents: 2550 })
  expect(parseUSD("1.234")).toEqual({ problem: "Use at most two decimals." })
  expect(parseUSD("-1")).toEqual({ problem: "The limit can't be negative." })
  expect(parseUSD("")).toEqual({ cents: null })
  expect(parseUSD("abc")).toHaveProperty("problem")
})

it("reads a rate with up to six decimals", () => {
  expect(parseRate("0.012345")).toEqual({ usd: "0.012345" })
  expect(parseRate("0")).toEqual({ usd: "0" })
  expect(parseRate("0.0123456")).toEqual({
    problem: "Use at most six decimals.",
  })
  expect(parseRate("1000.000001")).toHaveProperty("problem")
  expect(parseRate("1000")).toEqual({ usd: "1000" })
  expect(parseRate("")).toHaveProperty("problem")
})

it("reads a refund or credit amount within its limit", () => {
  expect(parseAmount("3", 500)).toEqual({ cents: 300 })
  expect(parseAmount("0", 500)).toHaveProperty("problem")
  expect(parseAmount("5.01", 500)).toEqual({ problem: "Enter at most $5.00." })
})

it("names a month and day in UTC", () => {
  expect(monthDay("2026-12-01T00:00:00Z")).toBe("1 December")
})
