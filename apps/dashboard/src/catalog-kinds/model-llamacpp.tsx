// A model the AI section deployed. Its service page links to the model
// page, where its cost, linked services and examples are; the catalog
// doesn't list it, since models are deployed from the Models page.
import { OpenModelPage } from "@/components/ai/models/open-model-page"
import { defineCatalogKind } from "@/lib/catalog-kinds"

export default defineCatalogKind({
  kind: "model-llamacpp",
  title: "llama.cpp model",
  group: "ai",
  hidden: true,
  blurb: "A GGUF model served by llama.cpp on CPU.",
  features: [],
  panels: [],
  actions: OpenModelPage,
})
