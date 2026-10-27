import { defineCatalogKind } from "@/lib/catalog-kinds"

export default defineCatalogKind({
  kind: "memcached",
  title: "Memcached",
  group: "cache",
  blurb:
    "Private disposable cache for trusted applications. No authentication or encryption. Restarting empties it; your app rebuilds it from its source. No backups or historical restores.",
  features: ["Up to 128 MiB", "UDP disabled", "Rebuild from your app"],
  panels: [],
})
