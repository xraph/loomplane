import { createFileRoute } from "@tanstack/react-router"

import { Runners } from "@/pages/runners"

export const Route = createFileRoute("/runners/")({ component: Runners })
