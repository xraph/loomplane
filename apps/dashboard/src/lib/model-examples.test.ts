import { expect, it } from "vitest"

import { snippets } from "@/lib/model-examples"

const code = (
  task: Parameters<typeof snippets>[0],
  id: string,
  model: string
) => snippets(task, model).find((s) => s.id === id)?.code ?? ""

it("keeps a served name with a quote in it one string", () => {
  const name = `it's "odd"\\`
  // Inside curl's single quotes, a quote closes and reopens the string.
  expect(code("chat", "curl", name)).toContain(
    `"model": "it'\\''s \\"odd\\"\\\\",`
  )
  expect(code("embedding", "curl", name)).toContain(
    `{"model": "it'\\''s \\"odd\\"\\\\", "input"`
  )
  expect(code("speech-to-text", "curl", name)).toContain(
    `-F 'model=it'\\''s "odd"\\' \\`
  )
  // Python and JavaScript read the JSON string as it is.
  expect(code("chat", "python", name)).toContain(`model="it's \\"odd\\"\\\\",`)
  expect(code("chat", "javascript", name)).toContain(
    `model: "it's \\"odd\\"\\\\",`
  )
})

it("puts the entry's prefix in front of each query", () => {
  const curl =
    snippets("embedding", "bge", "Represent this: ").find(
      (s) => s.id === "curl"
    )?.code ?? ""
  expect(curl).toContain(
    `"input": ["Represent this: How do I reset my password?"]`
  )
})
