// The dashboard's catalog kinds. Each kind brings its own module in
// src/catalog-kinds/<kind>.tsx, which describes the kind for the catalog
// and adds panels to the service page of every resource of that kind.
// Nothing else needs to change for a kind to appear: the modules are
// discovered here, once, when this file is first imported.
import {
  BoxesIcon,
  BoxIcon,
  DatabaseIcon,
  PlugIcon,
  ZapIcon,
  type LucideIcon,
} from "lucide-react"
import type { ComponentType } from "react"

import type { Binding, KindInfo } from "@/client/forge/loomplane/types"
import type { ProductKey } from "@/components/product-logos"
import {
  bindingConfig,
  isRecord,
  type DependencyChoice,
} from "@/lib/service-recovery"

/** The part of a resource binding's config the service page reads. */
export type ResourceConfigView = {
  kind: string
  provider: string
  composition?: {
    kind: string
    version: string
    dependencies: Record<
      string,
      {
        mode: "new" | "resource" | "external" | "off"
        resource?: string
        external?: string
        kind?: string
      }
    >
  }
  component?: {
    kind: string
    resource: string
    protection: "native" | "none" | "rebuildable" | "external"
    component: { name: string; role: "process" | "job" }
  }
}

/** What every panel of a service page is given. */
export type CatalogPanelProps = {
  projectId: string
  environmentId: string
  /** The resource's manifest name. */
  resource: string
  kind: string
  /** The resource's own binding. */
  binding: Binding
  config: ResourceConfigView
  /**
   * A composed resource's parts, each named <resource>--<part>: its
   * components and the dependencies provisioned for it (see
   * serviceBindings). Empty for a resource that isn't composed.
   */
  components: Binding[]
}

export type CatalogPanel = {
  /** Unique within the kind. */
  id: string
  /** The tab's label. */
  title: string
  /** Panels show in ascending order, then in the order they're listed. */
  order?: number
  /** Leave the panel out for resources it doesn't apply to. */
  when?: (props: CatalogPanelProps) => boolean
  component: ComponentType<CatalogPanelProps>
  /**
   * The number beside the tab's label, or undefined for none. The page
   * calls it on every render while the tab shows, always from the same
   * place, so it may use hooks.
   */
  useCount?: (props: CatalogPanelProps) => number | undefined
}

/** What a module's add dialog is given once the project is chosen. */
export type CatalogAddProps = {
  projectId: string
  projectName: string
  onClose: () => void
  /** Opens the plan that adds the service, for review. */
  onPlanned: (planId: string) => void
}

export type CatalogKindModule = {
  kind: string
  title: string
  logo?: ProductKey
  group: "database" | "cache" | "gateway" | "data"
  blurb: string
  features: string[]
  panels: CatalogPanel[]
  /**
   * The normal deployed state, or a function that names it from what is
   * deployed. Drift warnings always take precedence.
   */
  deployedLabel?: string | ((props: CatalogPanelProps) => string)
  /** Compact page with an inline header and environment in its subtitle. */
  serviceLayout?: "compact"
  /** Panel ID to open first, falling back to Overview when unavailable. */
  defaultPanel?: string
  /** Buttons at the right of the service page's header. */
  actions?: ComponentType<CatalogPanelProps>
  /** Drawn in place of a product logo when the kind has none. */
  icon?: ComponentType<{ className?: string }>
  /** Keeps the kind off the service catalog. */
  hidden?: boolean
  /**
   * Replaces the add page's own steps once the project is chosen. It
   * renders its own dialog, and calls onPlanned with the plan it made.
   */
  addDialog?: ComponentType<CatalogAddProps>
  /** A line under the resource's name on its service page. */
  subtitle?: (props: CatalogPanelProps, environment: string) => string
}

/** The icon of each group, for a kind with neither a logo nor an icon. */
export const groupIcons: Record<CatalogKindModule["group"], LucideIcon> = {
  database: DatabaseIcon,
  cache: ZapIcon,
  gateway: PlugIcon,
  data: BoxesIcon,
}

/** How the catalog names the targets a kind runs on. */
export const targetLabels: Record<string, string> = {
  server: "Docker",
  kubernetes: "Kubernetes",
}

/**
 * kindIcon is what a kind is drawn with when it has no product logo: the
 * module's own icon, then its group's, then a plain box.
 */
export function kindIcon(
  module: CatalogKindModule | undefined
): ComponentType<{ className?: string }> {
  if (!module) return BoxIcon
  return module.icon ?? groupIcons[module.group]
}

// Kind modules call defineCatalogKind while this file is still being
// evaluated, because the eager glob below imports them before the rest of
// it runs. A var is hoisted and can be read then; a const would still be
// uninitialized.
// eslint-disable-next-line no-var
var registered: Map<string, CatalogKindModule> | undefined

/**
 * defineCatalogKind registers a kind's module, which is the module's
 * default export. A kind registers once, and its panels' ids are distinct.
 */
export function defineCatalogKind(
  module: CatalogKindModule
): CatalogKindModule {
  const ids = new Set<string>()
  for (const panel of module.panels) {
    if (ids.has(panel.id))
      throw new Error(`${module.kind} has two panels called ${panel.id}`)
    ids.add(panel.id)
  }
  registered ??= new Map()
  if (registered.has(module.kind))
    throw new Error(`${module.kind} is registered twice`)
  registered.set(module.kind, module)
  return module
}

const modules = import.meta.glob<{
  default?: CatalogKindModule | CatalogKindModule[]
}>("../catalog-kinds/*.tsx", { eager: true })

for (const [path, file] of Object.entries(modules)) {
  if (!file.default)
    throw new Error(`${path} has no default export of its catalog kinds`)
  // A module that exports its kinds without defineCatalogKind is
  // registered here instead.
  for (const module of [file.default].flat())
    if (registered?.get(module.kind) !== module) defineCatalogKind(module)
}

/** catalogKind is the module of a kind, or undefined when none registered it. */
export function catalogKind(kind: string): CatalogKindModule | undefined {
  return registered?.get(kind)
}

/** catalogKinds lists every registered kind's module. */
export function catalogKinds(): CatalogKindModule[] {
  return [...(registered?.values() ?? [])]
}

/**
 * inCatalog reports whether a kind gets a card in the service catalog. A
 * kind provisioned only as another's dependency never does, and neither
 * does one whose module is hidden.
 */
export function inCatalog(kind: Pick<KindInfo, "name" | "placement">): boolean {
  return kind.placement !== "dependency" && !catalogKind(kind.name)?.hidden
}

/**
 * parseResourceConfig reads a resource binding's config, or returns
 * undefined when it has no kind and provider. A composition or component
 * that isn't shaped as expected is left out rather than trusted.
 */
export function parseResourceConfig(
  binding: Binding
): ResourceConfigView | undefined {
  const config = bindingConfig(binding)
  if (
    !config ||
    typeof config.kind !== "string" ||
    typeof config.provider !== "string"
  )
    return undefined
  const out: ResourceConfigView = { ...config } as ResourceConfigView
  const { composition, component } = config
  if (
    composition !== undefined &&
    !(isRecord(composition) && typeof composition.kind === "string")
  )
    delete out.composition
  if (
    component !== undefined &&
    !(
      isRecord(component) &&
      isRecord(component.component) &&
      typeof component.component.name === "string"
    )
  )
    delete out.component
  return out
}

/** dependencyChoices lists a composed resource's dependencies by name. */
export function dependencyChoices(
  config: ResourceConfigView
): [string, DependencyChoice][] {
  const deps = config.composition?.dependencies
  if (!isRecord(deps)) return []
  return Object.entries(deps)
    .filter((entry): entry is [string, DependencyChoice] => isRecord(entry[1]))
    .sort(([a], [b]) => a.localeCompare(b))
}

/**
 * componentName is the binding name of a part of a composed resource, the
 * API's planning.ComponentActionName.
 */
export function componentName(resource: string, part: string): string {
  return `${resource}--${part}`
}

/**
 * serviceBindings finds a resource's own binding and, when it is composed,
 * the bindings of its parts: its components, which name it as their
 * resource, and the dependencies provisioned for it, which its composition
 * names. A resource that only shares the name prefix isn't one of them.
 */
export function serviceBindings(
  bindings: Binding[],
  resource: string
): { binding?: Binding; components: Binding[] } {
  const resources = bindings.filter((b) => b.subject === "resource")
  const binding = resources.find((b) => b.name === resource)
  const config = binding && parseResourceConfig(binding)
  if (!config?.composition) return { binding, components: [] }
  const provisioned = new Set(
    dependencyChoices(config)
      .filter(([, choice]) => choice.mode === "new" && choice.resource)
      .map(([, choice]) => choice.resource)
  )
  return {
    binding,
    components: resources
      .filter((b) => {
        if (b.name === resource) return false
        if (provisioned.has(b.name)) return true
        return parseResourceConfig(b)?.component?.resource === resource
      })
      .sort((a, b) => a.name.localeCompare(b.name)),
  }
}

/**
 * catalogResources lists the resources a project's service pages are for:
 * every resource binding except the parts of a composed one, which show on
 * their parent's page.
 */
export function catalogResources(bindings: Binding[]): Binding[] {
  const resources = bindings.filter((b) => b.subject === "resource")
  const parts = new Set(
    resources.flatMap((b) => serviceBindings(resources, b.name).components)
  )
  return resources.filter((b) => !parts.has(b))
}
