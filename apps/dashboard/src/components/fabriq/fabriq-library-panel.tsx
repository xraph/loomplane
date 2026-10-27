import { FabriqStack } from "./fabriq-stack"
import { Panel } from "@/components/panel"
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
      <Panel className="gap-2">
        <h2 className="text-[14px] font-semibold">What your services get</h2>
        <p className="text-[12.5px] text-muted-foreground">
          Link a service to this resource in its manifest needs. Loomplane
          injects these credentials on its next release.
        </p>
        <ul className="grid gap-2 text-xs sm:grid-cols-2">
          {keys.map((key) => (
            <li key={key}>
              <code className="font-mono break-all">{key}</code>
            </li>
          ))}
        </ul>
        <p className="text-xs text-muted-foreground">
          FalkorDB and Elasticsearch credentials are included when you enable
          those engines.
        </p>
      </Panel>
      <p className="text-[12.5px] text-muted-foreground">
        Loomplane runs the stock Fabriq worker. It handles the entity types your
        app defines with <code className="font-mono">DefineDynamic</code>.
        Entities written as Go structs, and hooks such as{" "}
        <code className="font-mono">Validate</code> or embeddings, need your own
        binary built with{" "}
        <code className="font-mono">forgeext.WithWorker(true)</code>.
      </p>
    </div>
  )
}
