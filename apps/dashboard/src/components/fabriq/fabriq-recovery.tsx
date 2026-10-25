import {
  dependencyChoices,
  type CatalogPanelProps,
  type ResourceConfigView,
} from "@/lib/catalog-kinds"
import { ServiceRecovery } from "@/pages/service-catalog"

export function recoveryEngines(config: ResourceConfigView) {
  const choices = Object.fromEntries(dependencyChoices(config))
  return (
    [
      ["postgres", "Postgres"],
      ["redis", "Redis"],
    ] as const
  ).map(([id, name]) => {
    const choice = choices[id]
    return {
      name,
      resource:
        choice?.mode === "new" || choice?.mode === "resource"
          ? choice.resource
          : undefined,
      external: choice?.mode === "external",
    }
  })
}

export function FabriqRecovery(props: CatalogPanelProps) {
  const kubernetes = props.binding.provider === "kubernetes"
  return (
    <div className="grid gap-3">
      {kubernetes && (
        <p className="text-sm">
          Backups for these engines aren't available on Kubernetes yet.
        </p>
      )}
      {recoveryEngines(props.config).map((engine) => (
        <section
          key={engine.name}
          aria-label={`${engine.name} recovery`}
          className="min-w-0 space-y-2"
        >
          <h2 className="text-sm font-semibold">{engine.name}</h2>
          {engine.external ? (
            <p className="text-sm text-muted-foreground">
              Not managed by Loomplane: no backups or restores.
            </p>
          ) : !kubernetes && engine.resource ? (
            <ServiceRecovery
              project={props.projectId}
              environment={props.environmentId}
              resource={engine.resource}
            />
          ) : (
            !kubernetes && (
              <p role="status" className="text-sm text-muted-foreground">
                Recovery will appear after this engine is provisioned.
              </p>
            )
          )}
        </section>
      ))}
      <p className="text-xs text-muted-foreground">
        FalkorDB and Elasticsearch are rebuilt from Postgres. Use the Engines
        tab to rebuild their projections.
      </p>
    </div>
  )
}
