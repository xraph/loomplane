import { createFileRoute } from "@tanstack/react-router"

import { Nodes } from "@/pages/nodes"

export const Route = createFileRoute("/nodes/")({ component: Nodes })
