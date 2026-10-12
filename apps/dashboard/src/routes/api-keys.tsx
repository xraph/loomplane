import { createFileRoute } from "@tanstack/react-router"

import { ApiKeys } from "@/pages/api-keys"

export const Route = createFileRoute("/api-keys")({ component: ApiKeys })
