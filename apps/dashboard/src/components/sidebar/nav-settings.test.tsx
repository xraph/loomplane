import { expect, it } from "vitest"

import { mergeSettingsGroups } from "./nav-settings"

const link = (title: string) => ({ title, icon: null, link: <a /> })

it("merges groups with the same label and keeps the rest", () => {
  const merged = mergeSettingsGroups(
    [
      { label: "Workspace", items: [link("Members")] },
      { label: "Platform", items: [] },
    ],
    [
      { label: "Workspace", items: [link("Billing")] },
      { label: "Platform", items: [link("Billing")] },
      { label: "Other", items: [link("X")] },
    ]
  )
  expect(merged.map((g) => [g.label, g.items.map((i) => i.title)])).toEqual([
    ["Workspace", ["Members", "Billing"]],
    ["Platform", ["Billing"]],
    ["Other", ["X"]],
  ])
})

it("leaves the base groups as they were", () => {
  const base = [{ label: "Workspace", items: [link("Members")] }]
  mergeSettingsGroups(base, [{ label: "Workspace", items: [link("Billing")] }])
  expect(base[0].items).toHaveLength(1)
})
