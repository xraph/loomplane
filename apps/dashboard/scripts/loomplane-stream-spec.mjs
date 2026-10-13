#!/usr/bin/env node
// Keep Loomplane's generated streaming contracts independent of the host
// that published them. Forge owns channel schemas and cache bindings.
import { writeFile } from "node:fs/promises"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

let raw = ""
for await (const chunk of process.stdin) raw += chunk
const spec = JSON.parse(raw)
spec.channels = Object.fromEntries(
  Object.entries(spec.channels ?? {}).filter(([, channel]) =>
    channel.address?.startsWith("/api/")
  )
)
const retained = new Set(Object.keys(spec.channels))
spec.operations = Object.fromEntries(
  Object.entries(spec.operations ?? {}).filter(([, operation]) =>
    retained.has(operation.channel?.$ref?.split("/").at(-1))
  )
)
spec.servers = {}
spec.components ??= {}
spec.components.schemas ??= {}
for (const channel of Object.values(spec.channels)) {
  delete channel.servers
  const bindings = channel["x-forge-stream"] ?? []
  for (const [key, message] of Object.entries(channel.messages ?? {})) {
    if (!message.payload || message.payload.$ref) continue
    const entity = bindings.find((binding) => binding.message === key)?.entityType
    const name = entity ?? `Realtime${key.split(/[.\-]/).map((part) =>
      part[0].toUpperCase() + part.slice(1)
    ).join("")}`
    const previous = spec.components.schemas[name]
    if (previous && JSON.stringify(previous) !== JSON.stringify(message.payload)) {
      throw new Error(`Conflicting stream schema: ${name}`)
    }
    spec.components.schemas[name] = message.payload
    message.payload = { $ref: `#/components/schemas/${name}` }
  }
}
const output = join(
  dirname(fileURLToPath(import.meta.url)),
  "..", "specs", "loomplane.asyncapi.json"
)
await writeFile(output, `${JSON.stringify(spec, null, 2)}\n`)
console.log(`wrote ${retained.size} channels to ${output}`)
