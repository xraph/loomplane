/** The reasons a person can give when they report an agent answer. */
export const flagReasons = [
  { value: "offensive", label: "Offensive" },
  { value: "harmful", label: "Harmful" },
  { value: "inaccurate", label: "Inaccurate" },
  { value: "other", label: "Something else" },
] as const

export const FLAG_NOTE_MAX = 1000

/** reasonLabel is a reason as people read it. */
export function reasonLabel(reason: string): string {
  return flagReasons.find((r) => r.value === reason)?.label ?? reason
}
