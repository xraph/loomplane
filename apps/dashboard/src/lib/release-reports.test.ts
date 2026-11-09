import { expect, test } from "vitest"

import {
  gateNames,
  isActive,
  mayCancelReport,
  stateLabel,
  stateTone,
} from "./release-reports"

test("names the seven gates like the server", () => {
  expect(Object.keys(gateNames)).toHaveLength(7)
  expect(gateNames[4]).toBe("staging soak")
  expect(gateNames[2]).toBe("promotes what staging runs")
  expect(gateNames[6]).toBe("nothing open on staging")
})

test("knows which states are still active", () => {
  for (const s of ["planning", "reviewing", "promoting", "waiting_for_person"])
    expect(isActive(s)).toBe(true)
  for (const s of [
    "promoted",
    "approved",
    "rejected",
    "stale",
    "expired",
    "superseded",
    "blocked",
    "stopped",
    "failed",
    "cancelled",
  ])
    expect(isActive(s)).toBe(false)
})

test("words and tones", () => {
  expect(stateLabel("waiting_for_person")).toBe("Waiting for a person")
  expect(stateLabel("promoted")).toBe("Promoted by the agent")
  expect(stateLabel("something_new")).toBe("something_new")
  expect(stateTone("waiting_for_person")).toBe("warning")
  expect(stateTone("promoted")).toBe("success")
  expect(stateTone("reviewing")).toBe("progress")
  expect(stateTone("failed")).toBe("danger")
  expect(stateTone("cancelled")).toBe("neutral")
})

test("an approver or the requester holding agent:use may cancel", () => {
  const active = { state: "reviewing", requestedBy: "usr_1" }
  const held =
    (...p: string[]) =>
    (x: string) =>
      p.includes(x)
  expect(
    mayCancelReport(active, {
      userId: "usr_2",
      can: held("deployment:approve"),
    })
  ).toBe(true)
  expect(
    mayCancelReport(active, { userId: "usr_1", can: held("agent:use") })
  ).toBe(true)
  // Someone else who only holds agent:use, or the requester without it.
  expect(
    mayCancelReport(active, { userId: "usr_2", can: held("agent:use") })
  ).toBe(false)
  expect(mayCancelReport(active, { userId: "usr_1", can: held() })).toBe(false)
  // While the agent is promoting, its approval may be landing: the server
  // refuses a cancel, so nobody is offered one.
  expect(
    mayCancelReport(
      { state: "promoting", requestedBy: "usr_1" },
      { userId: "usr_1", can: held("deployment:approve", "agent:use") }
    )
  ).toBe(false)
  // A finished report has nothing to cancel.
  expect(
    mayCancelReport(
      { state: "promoted", requestedBy: "usr_1" },
      { userId: "usr_1", can: held("deployment:approve", "agent:use") }
    )
  ).toBe(false)
})
