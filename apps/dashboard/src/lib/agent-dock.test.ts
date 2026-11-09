import { expect, it } from "vitest"

import type { Conversation } from "@/client/forge/loomplane/types"
import {
  DOCK_MAX,
  DOCK_MIN,
  clampWidth,
  dateGroup,
  filterConversations,
  groupConversations,
  noFilter,
  readDockPrefs,
  readWorkspaceAgent,
  writeDockPrefs,
} from "@/lib/agent-dock"
import { toolLabel } from "@/components/assistant/tool-step"
import { grammarOf } from "@/lib/highlight"
import { sidebarOpenAtLoad } from "@/lib/sidebar-state"

const now = new Date("2026-11-05T15:00:00").getTime()

function convo(over: Partial<Conversation>): Conversation {
  return {
    id: "c",
    orgId: "org",
    projectId: "",
    title: "",
    startedBy: "usr_1",
    status: "completed",
    lastTaskId: "t",
    turns: 1,
    pinned: false,
    createdAt: "2026-11-05T10:00:00",
    updatedAt: "2026-11-05T10:00:00",
    ...over,
  }
}

const names = new Map([
  ["p1", "api"],
  ["p2", "web"],
])

it("names the day a conversation was last updated in", () => {
  expect(dateGroup("2026-11-05T09:00:00", now)).toBe("Today")
  expect(dateGroup("2026-11-04T23:59:00", now)).toBe("Yesterday")
  expect(dateGroup("2026-10-31T12:00:00", now)).toBe("Previous 7 days")
  expect(dateGroup("2026-10-10T12:00:00", now)).toBe("Previous 30 days")
  expect(dateGroup("2026-06-01T12:00:00", now)).toBe("Older")
})

it("groups by project, the most recently active first, pins on top", () => {
  const list = [
    convo({ id: "a", projectId: "p1", updatedAt: "2026-11-05T09:00:00" }),
    convo({ id: "b", projectId: "p2", updatedAt: "2026-11-05T11:00:00" }),
    convo({ id: "c", projectId: "", updatedAt: "2026-11-05T10:00:00" }),
    convo({ id: "d", projectId: "gone", updatedAt: "2026-11-01T10:00:00" }),
    convo({ id: "e", projectId: "p1", updatedAt: "2026-11-05T12:00:00" }),
  ]
  const groups = groupConversations(list, "project", names, new Set(["b"]), now)
  expect(
    groups.map((g) => [g.label, g.conversations.map((c) => c.id)])
  ).toEqual([
    ["Pinned", ["b"]],
    ["api", ["e", "a"]],
    ["Whole workspace", ["c"]],
    ["A deleted project", ["d"]],
  ])
})

it("groups by date in calendar order", () => {
  const list = [
    convo({ id: "old", updatedAt: "2026-01-01T10:00:00" }),
    convo({ id: "today", updatedAt: "2026-11-05T10:00:00" }),
    convo({ id: "yday", updatedAt: "2026-11-04T10:00:00" }),
  ]
  expect(
    groupConversations(list, "date", names, new Set(), now).map((g) => g.label)
  ).toEqual(["Today", "Yesterday", "Older"])
})

it("filters by words, status and project", () => {
  const list = [
    convo({
      id: "a",
      title: "Why is it slow?",
      projectId: "p1",
      status: "running",
    }),
    convo({ id: "b", title: "Release", projectId: "p2", status: "waiting" }),
    convo({ id: "c", title: "", projectId: "", status: "failed" }),
  ]
  const ids = (f: Partial<typeof noFilter>) =>
    filterConversations(list, { ...noFilter, ...f }, names).map((c) => c.id)
  expect(ids({ query: "SLOW" })).toEqual(["a"])
  // A project's name matches too, and an empty title reads as Untitled.
  expect(ids({ query: "web" })).toEqual(["b"])
  expect(ids({ query: "untitled" })).toEqual(["c"])
  expect(ids({ status: "working" })).toEqual(["a"])
  expect(ids({ status: "waiting" })).toEqual(["b"])
  expect(ids({ project: "workspace" })).toEqual(["c"])
  expect(ids({ project: "p1" })).toEqual(["a"])
})

it("keeps a docked width within its bounds and leaves the page room", () => {
  expect(clampWidth(100, 1600)).toBe(DOCK_MIN)
  expect(clampWidth(5000, 1600)).toBe(DOCK_MAX)
  expect(clampWidth(700, 1000)).toBe(520)
  // A window too small for both still gets the narrowest panel.
  expect(clampWidth(700, 600)).toBe(DOCK_MIN)
})

it("reads back what it wrote, and defaults for anything it can't read", () => {
  expect(readDockPrefs()).toEqual({
    mode: "docked",
    width: 420,
    open: false,
    groupBy: "project",
  })
  writeDockPrefs({ mode: "floating", width: 500, open: true, groupBy: "date" })
  expect(readDockPrefs()).toEqual({
    mode: "floating",
    width: 500,
    open: true,
    groupBy: "date",
  })
  localStorage.setItem(
    "loomplane.agent.dock",
    JSON.stringify({ mode: "sideways", width: "wide", open: "yes" })
  )
  expect(readDockPrefs()).toMatchObject({
    mode: "docked",
    width: 420,
    open: false,
  })
  localStorage.setItem("loomplane.agent.org", "{not json")
  expect(readWorkspaceAgent("org")).toEqual({})
})

it("opens the sidebar unless it was left collapsed", () => {
  expect(sidebarOpenAtLoad("")).toBe(true)
  expect(sidebarOpenAtLoad("theme=dark; sidebar_state=false")).toBe(false)
  expect(sidebarOpenAtLoad("sidebar_state=true")).toBe(true)
  expect(sidebarOpenAtLoad("other_sidebar_state=false")).toBe(true)
})

it("colours the languages an infrastructure answer uses", () => {
  expect(grammarOf("YAML")).toBe("yaml")
  expect(grammarOf("yml")).toBe("yaml")
  expect(grammarOf("sh")).toBe("bash")
  expect(grammarOf("terraform")).toBe("hcl")
  expect(grammarOf("brainfuck")).toBeUndefined()
  expect(grammarOf(undefined)).toBeUndefined()
})

it("says what a tool call did in a few words", () => {
  expect(toolLabel("list_deployments", false)).toBe("Listed deployments")
  expect(toolLabel("list_deployments", true)).toBe("Listing deployments")
  expect(toolLabel("get_repository_state", false)).toBe(
    "Checked the repositories"
  )
  expect(toolLabel("get_deployment", true)).toBe("Looking up deployment")
  expect(toolLabel("whoami", false)).toBe("Checked who you are")
  expect(toolLabel("deployed", false)).toBe("Checked what's deployed")
  expect(toolLabel("mystery_op", true)).toBe("Running mystery_op")
})
