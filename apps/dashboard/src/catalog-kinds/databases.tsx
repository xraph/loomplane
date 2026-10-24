// The five engines the catalog has always run. Each keeps its backups,
// test restores and recovery copies on a Recovery panel.
import {
  defineCatalogKind,
  type CatalogKindModule,
  type CatalogPanel,
} from "@/lib/catalog-kinds"
import { protectionOf } from "@/lib/service-recovery"
import { ServiceRecovery } from "@/pages/service-catalog"

const recovery: CatalogPanel = {
  id: "recovery",
  title: "Recovery",
  // Only where Loomplane backs the engine up on its target: not Redis or
  // Valkey on Kubernetes, and not a managed or external database.
  when: (props) => protectionOf(props.binding)?.protection === "native",
  component: (props) => (
    <ServiceRecovery
      project={props.projectId}
      environment={props.environmentId}
      resource={props.resource}
    />
  ),
}

const databaseFeatures = ["Retained volume", "Daily backups", "Test restores"]
const cacheFeatures = ["AOF every second", "RDB snapshots", "Up to 128 MiB"]

const engines: Omit<CatalogKindModule, "panels">[] = [
  {
    kind: "nats",
    title: "NATS JetStream",
    group: "data",
    blurb:
      "Persistent messaging on one private server. Back up file streams and durable consumers, then verify them in an isolated copy.",
    features: [
      "Retained file store",
      "Messages and consumers",
      "Test restores",
    ],
  },
  {
    kind: "postgres",
    title: "PostgreSQL",
    logo: "postgresql",
    group: "database",
    blurb:
      "The relational database most apps reach for, with a restricted application role and backups you can test-restore.",
    features: databaseFeatures,
  },
  {
    kind: "mysql",
    title: "MySQL",
    logo: "mysql",
    group: "database",
    blurb:
      "Relational database with a restricted application role and native backups you can test-restore.",
    features: databaseFeatures,
  },
  {
    kind: "mariadb",
    title: "MariaDB",
    logo: "mariadb",
    group: "database",
    blurb:
      "MySQL-compatible relational database, with the same role, volume and backup setup.",
    features: databaseFeatures,
  },
  {
    kind: "redis",
    title: "Redis",
    logo: "redis",
    group: "cache",
    blurb:
      "In-memory cache with AOF persistence every second and native RDB snapshots. No failover in this profile.",
    features: cacheFeatures,
  },
  {
    kind: "valkey",
    title: "Valkey",
    group: "cache",
    blurb:
      "Open source, Redis-compatible cache with the same persistence and snapshot setup.",
    features: cacheFeatures,
  },
]

export default engines.map((engine) =>
  defineCatalogKind({ ...engine, panels: [recovery] })
)
