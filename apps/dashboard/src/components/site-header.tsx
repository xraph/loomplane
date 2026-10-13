// The bar above every page of the dashboard: the shared top bar, with the
// organization first in the breadcrumbs, what's in flight, search opening
// the command menu, and the New deployment button.
import { PlusIcon } from "lucide-react"

import { EnvBadge, Thread } from "@/components/runs"
import { useShell } from "@/components/shell"
import { TopBar } from "@/components/top-bar"
import { Button } from "@/components/ui/button"
import {
  Item,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { duration } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { elapsed, isLive } from "@/lib/runs"
import { useNow, useRuns } from "@/lib/use-runs"

export type Crumb = {
  label: string
  to?:
    | "/"
    | "/projects"
    | "/deployments"
    | "/infrastructure"
    | "/members"
    | "/integrations"
    | "/variables"
    | "/activity"
}

export function SiteHeader({ crumbs }: { crumbs: Crumb[] }) {
  const { openCommand, openDeploy } = useShell()
  const { session, organizations, can } = useOrganization()
  const org = organizations.find((o) => o.id === session.orgId)
  return (
    <TopBar
      onSearch={openCommand}
      crumbs={[
        ...(org ? [{ label: org.name, link: <Link to="/" /> }] : []),
        ...crumbs.map((c) => ({
          label: c.label,
          link: c.to ? <Link to={c.to} /> : undefined,
        })),
      ]}
      before={<InFlight />}
      after={
        can("deployment:create") && (
          <Button
            size="sm"
            className="rounded-full"
            onClick={() => openDeploy()}
          >
            <PlusIcon />
            <span className="hidden sm:inline">New deployment</span>
            <span className="sm:hidden">Deploy</span>
          </Button>
        )
      }
    />
  )
}

function InFlight() {
  const { runs } = useRuns()
  const now = useNow()
  const live = runs.filter(isLive)
  if (live.length === 0) return null
  const waiting = live.filter((r) => r.status === "waiting").length
  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="gap-2 rounded-full text-muted-foreground"
          />
        }
      >
        <span className="relative flex size-2">
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-weld opacity-60 motion-reduce:animate-none" />
          <span className="relative inline-flex size-2 rounded-full bg-weld" />
        </span>
        <span className="tabular-nums">
          {live.length}
          <span className="hidden sm:inline"> in flight</span>
          {waiting > 0 && (
            <span className="hidden lg:inline">, {waiting} waiting on you</span>
          )}
        </span>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-96 gap-3 p-3">
        <PopoverHeader>
          <PopoverTitle>In flight</PopoverTitle>
          <PopoverDescription>
            Deployments running or waiting right now.
          </PopoverDescription>
        </PopoverHeader>
        <ItemGroup className="gap-1">
          {live.map((r) => (
            <Item
              key={r.id}
              size="xs"
              className="flex-col items-stretch gap-1.5"
              render={<Link to="/deployments/$id" params={{ id: r.id }} />}
            >
              <ItemTitle className="w-full">
                <span className="truncate">
                  {r.project?.name ?? "A deleted project"}
                </span>
                <EnvBadge env={r.environment} />
                <span className="ml-auto text-xs font-normal text-muted-foreground tabular-nums">
                  {duration(elapsed(r, now))}
                </span>
              </ItemTitle>
              <Thread steps={r.thread} />
              <ItemDescription className="text-xs">{r.phase}</ItemDescription>
            </Item>
          ))}
        </ItemGroup>
      </PopoverContent>
    </Popover>
  )
}
