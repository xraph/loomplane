// A model the AI section deployed. Its service page links to the model
// page, where its cost, linked services and examples are; the catalog
// doesn't list it, since models are deployed from the Models page.
import { OpenModelPage } from "@/components/ai/models/open-model-page"
import { defineCatalogKind } from "@/lib/catalog-kinds"

export default defineCatalogKind({
  kind: "model-tei",
  title: "TEI model",
  group: "ai",
  hidden: true,
  blurb:
    "Embedding and reranking models served by Text Embeddings Inference on CPU.",
  features: [],
  panels: [],
  actions: OpenModelPage,
})
