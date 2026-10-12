import { createFileRoute } from "@tanstack/react-router"

import { Variables } from "@/pages/variables"

export const Route = createFileRoute("/variables")({ component: Variables })
