import { createFileRoute } from "@tanstack/react-router"
import { ServiceCatalog } from "@/pages/service-catalog"
export const Route = createFileRoute("/services")({ component: ServiceCatalog })
