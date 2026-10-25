import { FabriqStack } from "./fabriq-stack"
import { dependencyChoices, type CatalogPanelProps } from "@/lib/catalog-kinds"

export function FabriqLibraryPanel(props: CatalogPanelProps) {
  const choices = Object.fromEntries(dependencyChoices(props.config))
  const keys = [
    "FABRIQ_POSTGRES_DSN",
    "FABRIQ_REDIS_ADDR",
    "FABRIQ_REDIS_USERNAME",
    "FABRIQ_REDIS_PASSWORD",
  ]
  if (choices.falkordb && choices.falkordb.mode !== "off")
    keys.push(
      "FABRIQ_FALKORDB_ADDR",
      "FABRIQ_FALKORDB_USERNAME",
      "FABRIQ_FALKORDB_PASSWORD"
    )
  if (choices.elasticsearch && choices.elasticsearch.mode !== "off")
    keys.push(
      "FABRIQ_ELASTICSEARCH_ADDRS",
      "FABRIQ_ELASTICSEARCH_USERNAME",
      "FABRIQ_ELASTICSEARCH_PASSWORD"
    )
  return (
    <div className="grid gap-3">
      <FabriqStack {...props} />
      <section className="rounded-xl border bg-card p-3">
        <h2 className="text-sm font-semibold">What your services get</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          Link a service to this resource in its manifest needs. Loomplane
          injects these credentials on its next release.
        </p>
        <ul className="mt-2 grid gap-1 text-xs sm:grid-cols-2">
          {keys.map((key) => (
            <li key={key}>
              <code className="break-all">{key}</code>
            </li>
          ))}
        </ul>
      </section>
      <p className="text-sm text-muted-foreground">
        Loomplane runs the stock Fabriq worker. It handles the entity types your
        app defines with DefineDynamic. Entities written as Go structs, and
        hooks such as Validate or embeddings, need your own binary built with
        forgeext.WithWorker(true).
      </p>
    </div>
  )
}
