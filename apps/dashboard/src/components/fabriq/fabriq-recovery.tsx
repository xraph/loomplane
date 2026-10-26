import {
  dependencyChoices,
  type CatalogPanelProps,
  type ResourceConfigView,
} from "@/lib/catalog-kinds"
import { FabriqEmpty } from "./fabriq-empty"
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
  const engines = recoveryEngines(props.config)
  const kubernetes = props.binding.provider === "kubernetes"
  return (
    <div className="grid gap-3">
      {kubernetes && (
        <p className="text-[13px]">
          Backups for these engines aren't available on Kubernetes yet.
        </p>
      )}
      {engines.map((engine) => (
        <section
          key={engine.name}
          aria-label={`${engine.name} recovery`}
          className="min-w-0 space-y-2"
        >
          {(engine.external || kubernetes || !engine.resource) && (
            <h2 className="text-[15px] font-semibold">{engine.name}</h2>
          )}
          {engine.external ? (
            <p className="text-[13px] text-muted-foreground">
              Not managed by Loomplane: no backups or restores.
            </p>
          ) : !kubernetes && engine.resource ? (
            <ServiceRecovery
              embeddedTitle={engine.name}
              project={props.projectId}
              environment={props.environmentId}
              resource={engine.resource}
            />
          ) : (
            !kubernetes && (
              <FabriqEmpty
                title="Engine not provisioned"
                description="Recovery will appear after this engine is provisioned."
              />
            )
          )}
        </section>
      ))}
      <p className="text-xs text-muted-foreground">
        FalkorDB and Elasticsearch are rebuilt from Postgres. Use the Engines
        tab to rebuild their projections.
      </p>
      {!kubernetes && !engines.some((engine) => engine.external) && (
        <details className="grid gap-2 rounded-xl bg-card px-4 py-3.5 text-[12.5px] text-muted-foreground ring-1 ring-border">
          <summary className="cursor-pointer focus-visible:outline-2 focus-visible:outline-ring">
            Other targets and engine sources
          </summary>
          <p className="mt-2">
            <span className="font-medium text-foreground">Kubernetes:</span>{" "}
            Backups for these engines aren't available on Kubernetes yet.
          </p>
          <p className="mt-2">
            <span className="font-medium text-foreground">
              External endpoint:
            </span>{" "}
            Not managed by Loomplane: no backups or restores.
          </p>
        </details>
      )}
    </div>
  )
}
