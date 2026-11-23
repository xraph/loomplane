import type { Manifest, ProviderResource } from "@/client/forge/loomplane/types"
import { freeName, takenNames } from "./catalog-add"

const normalizeKind = (kind: string) => (kind === "valkey" ? "redis" : kind)

/** Returns the attached provider resource ID for an external resource entry, if set. */
export function existingResourceId(
  entry: Record<string, unknown> | undefined
): string | undefined {
  if (!entry) return undefined
  const id = entry.existing_resource ?? entry.existingResource
  return typeof id === "string" && id.trim() !== "" ? id : undefined
}

/** Reports whether two resource kinds map to the same backing engine. */
export function compatibleResourceKind(a: string, b: string): boolean {
  return normalizeKind(a) === normalizeKind(b)
}

/**
 * Reports whether a manifest resource runs in the given environment, matching
 * the planner's opt-in rules for existing_resource attachments.
 */
export function resourceEnabledIn(
  entry: Record<string, unknown> | undefined,
  environment: string
): boolean {
  if (!entry) return true
  const envs = entry.environments as
    | Record<string, { enabled?: boolean } | undefined>
    | undefined
  const envEnabled = envs?.[environment]?.enabled
  if (existingResourceId(entry) && envs && Object.keys(envs).length > 0) {
    return envEnabled === true
  }
  if (envEnabled !== undefined) return envEnabled !== false
  return entry.enabled !== false
}

/** Lists services and composed resources that currently reference a resource. */
export function resourceConsumers(manifest: Manifest, name: string) {
  const services = Object.entries(manifest.services ?? {})
    .filter(
      ([, entry]) => Array.isArray(entry?.needs) && entry.needs.includes(name)
    )
    .map(([key]) => key)
  const resources = Object.entries(manifest.resources ?? {})
    .filter(([, entry]) =>
      Object.values(entry?.uses ?? {}).some(
        (use) => (use as { resource?: string } | undefined)?.resource === name
      )
    )
    .map(([key]) => key)
  return { services, resources }
}

/**
 * Replaces a manifest resource with another project resource or an external
 * workspace resource, or removes it when replacement is omitted.
 */
export function replaceResource(
  manifest: Manifest,
  name: string,
  replacement?: string | ProviderResource,
  options?: { environments?: string[]; ignoreKind?: boolean }
): Manifest {
  const next = structuredClone(manifest)
  const resources = next.resources ?? {}
  const original = resources[name] as Record<string, unknown> | undefined
  if (!original) throw new Error(`${name} is no longer in the manifest.`)
  const consumers = resourceConsumers(next, name)
  let target: string | undefined
  if (typeof replacement === "string") {
    if (replacement === name || !Object.hasOwn(resources, replacement))
      throw new Error("Choose another resource from the project.")
    target = replacement
  } else if (replacement) {
    const origEnvs =
      (original.environments as Record<string, unknown> | undefined) ?? {}
    const envNames = [
      ...new Set([...(options?.environments ?? []), ...Object.keys(origEnvs)]),
    ]
    const disabledSomewhere =
      original.enabled === false ||
      envNames.some((env) => !resourceEnabledIn(original, env))
    const environments =
      disabledSomewhere && envNames.length > 0
        ? Object.fromEntries(
            envNames.map((env) => [
              env,
              { enabled: resourceEnabledIn(original, env) },
            ])
          )
        : undefined
    // A managed binding cannot become an external attachment under the same
    // name, whereas an existing attachment can switch targets in place.
    if (existingResourceId(original)) {
      target = name
    } else {
      const stem = name.slice(0, 26).replace(/-+$/, "") || "resource"
      target = freeName(`${stem}-existing`, takenNames(next))
    }
    resources[target] = {
      kind: original.kind,
      existing_resource: replacement.id,
      ...(environments ? { environments } : {}),
    }
  }
  if (!target && consumers.resources.length)
    throw new Error(
      `Choose a replacement for ${consumers.resources.join(", ")} first. Removing its dependency would provision a new one.`
    )
  if (target !== name) {
    for (const service of consumers.services) {
      const entry = next.services![service]
      const nextNeeds = [
        ...new Set(
          (entry.needs as string[]).flatMap((n) =>
            n !== name ? [n] : target ? [target] : []
          )
        ),
      ]
      if (nextNeeds.length > 0) entry.needs = nextNeeds
      else delete entry.needs

      const needOptions = entry.need_options as
        | Record<string, Record<string, unknown>>
        | undefined
      if (needOptions?.[name]) {
        if (target) {
          const merged = { ...needOptions[target] }
          for (const [key, value] of Object.entries(needOptions[name])) {
            if (key in merged && merged[key] !== value)
              throw new Error(
                `Resolve the conflicting ${key} dependency option for ${service} before switching resources.`
              )
            merged[key] = value
          }
          needOptions[target] = merged
        }
        delete needOptions[name]
        if (Object.keys(needOptions).length === 0) delete entry.need_options
      }
    }
    for (const consumer of consumers.resources) {
      const uses = resources[consumer]?.uses as
        | Record<string, { resource?: string }>
        | undefined
      for (const use of Object.values(uses ?? {}))
        if (use.resource === name) use.resource = target
    }
    delete resources[name]
    if (Object.keys(resources).length === 0) delete next.resources
  }
  if (!replacement && options?.ignoreKind) {
    const origKind = String(original.kind ?? "")
    const hasSibling = Object.values(next.resources ?? {}).some(
      (r) =>
        r &&
        compatibleResourceKind(
          String((r as { kind?: string }).kind ?? ""),
          origKind
        )
    )
    if (origKind && !hasSibling) {
      const ignored = next.ignore?.resources ?? []
      if (!ignored.includes(origKind)) {
        next.ignore = {
          ...next.ignore,
          resources: [...ignored, origKind],
        }
      }
    }
  }
  return next
}

/**
 * Updates a manifest resource's environment enablement, version, environment
 * settings, and consumer service wiring in one manifest revision.
 */
export function updateResourceManifest(
  manifest: Manifest,
  name: string,
  environment: string,
  patch: {
    enabled?: boolean
    environments?: string[]
    version?: string
    settings?: Record<string, string>
    neededBy?: string[]
  }
): Manifest {
  const next = structuredClone(manifest)
  const entry = next.resources?.[name] as Record<string, unknown> | undefined
  if (!entry) throw new Error(`${name} is no longer in the manifest.`)
  const isExisting = !!existingResourceId(entry)

  if (patch.enabled !== undefined) {
    if (isExisting) {
      const existingEnvs =
        (entry.environments as
          | Record<string, { enabled?: boolean }>
          | undefined) ?? {}
      const envNames = [
        ...new Set([
          ...(patch.environments ?? []),
          ...Object.keys(existingEnvs),
          environment,
        ]),
      ]
      const nextEnabled: Record<string, boolean> = {}
      for (const env of envNames) {
        nextEnabled[env] =
          env === environment ? patch.enabled : resourceEnabledIn(entry, env)
      }
      if (envNames.every((env) => nextEnabled[env])) {
        delete entry.environments
      } else {
        entry.environments = Object.fromEntries(
          envNames.map((env) => [env, { enabled: nextEnabled[env] }])
        )
      }
    } else {
      const envs =
        (entry.environments as
          | Record<string, Record<string, unknown>>
          | undefined) ?? {}
      entry.environments = {
        ...envs,
        [environment]: {
          ...envs[environment],
          enabled: patch.enabled,
        },
      }
    }
  }

  if (!isExisting) {
    if (patch.version !== undefined) {
      if (patch.version === "") delete entry.version
      else entry.version = patch.version
    }
    if (patch.settings !== undefined) {
      const envs =
        (entry.environments as
          | Record<string, Record<string, unknown>>
          | undefined) ?? {}
      const envBlock = { ...(envs[environment] ?? {}) }
      const settings = {
        ...((envBlock.settings as Record<string, string> | undefined) ?? {}),
      }
      for (const [k, v] of Object.entries(patch.settings)) {
        if (v === "") delete settings[k]
        else settings[k] = v
      }
      if (Object.keys(settings).length > 0) envBlock.settings = settings
      else delete envBlock.settings

      if (Object.keys(envBlock).length > 0) {
        entry.environments = { ...envs, [environment]: envBlock }
      } else {
        const restEnvs = { ...envs }
        delete restEnvs[environment]
        if (Object.keys(restEnvs).length > 0) entry.environments = restEnvs
        else delete entry.environments
      }
    }
  }

  if (patch.neededBy !== undefined) {
    for (const [svcName, svc] of Object.entries(next.services ?? {})) {
      const service = svc as Record<string, unknown>
      const want = patch.neededBy.includes(svcName)
      const needs = Array.isArray(service.needs)
        ? (service.needs as string[])
        : []
      const has = needs.includes(name)
      if (want && !has) {
        service.needs = [...needs, name]
      } else if (!want && has) {
        const filtered = needs.filter((n) => n !== name)
        if (filtered.length > 0) service.needs = filtered
        else delete service.needs

        const needOptions = service.need_options as
          | Record<string, unknown>
          | undefined
        if (needOptions && name in needOptions) {
          delete needOptions[name]
          if (Object.keys(needOptions).length === 0) delete service.need_options
        }
      }
    }
  }

  return next
}
