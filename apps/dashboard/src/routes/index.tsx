import { createFileRoute } from "@tanstack/react-router"

import { Projects } from "@/pages/projects"

export const Route = createFileRoute("/")({ component: Projects })
