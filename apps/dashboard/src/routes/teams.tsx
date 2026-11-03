import { createFileRoute } from "@tanstack/react-router"

import { Teams } from "@/pages/teams"

export const Route = createFileRoute("/teams")({ component: Teams })
