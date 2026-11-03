import { createFileRoute } from "@tanstack/react-router"

import { Webhooks } from "@/pages/webhooks"

export const Route = createFileRoute("/webhooks")({ component: Webhooks })
