import { createFileRoute } from "@tanstack/react-router"

import { Members } from "@/pages/members"

export const Route = createFileRoute("/members")({ component: Members })
