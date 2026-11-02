// A model the AI section deployed. Its service page links to the model
// page, where its cost, linked services and examples are; the catalog
// doesn't list it, since models are deployed from the Models page.
import { OpenModelPage } from "@/components/ai/models/open-model-page"
import { defineCatalogKind } from "@/lib/catalog-kinds"

export default defineCatalogKind({
  kind: "model-vllm",
  title: "vLLM model",
  group: "ai",
  hidden: true,
  blurb: "Chat, embedding and speech models served by vLLM on a GPU.",
  features: [],
  panels: [],
  actions: OpenModelPage,
})
