// PostgreSQL 16 with TimescaleDB, pgvector and PostGIS. It backs up and
// restores like the other Postgres engines, so it keeps their Recovery panel.
import { recovery } from "@/catalog-kinds/databases"
import { defineCatalogKind } from "@/lib/catalog-kinds"

export default defineCatalogKind({
  kind: "timescaledb",
  title: "TimescaleDB",
  logo: "postgresql",
  group: "database",
  blurb:
    "PostgreSQL 16 with TimescaleDB, pgvector and PostGIS, the database Fabriq runs on. It keeps the restricted application role and backups you can test-restore.",
  features: ["Retained volume", "Daily backups", "Test restores"],
  panels: [recovery],
})
