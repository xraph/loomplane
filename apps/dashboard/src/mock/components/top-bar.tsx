// The mock's top bar: the shared one, with what's in flight and the deploy
// button in its slots.
import { PlusIcon } from "lucide-react"
import type { ReactNode } from "react"

import { TopBar as SharedTopBar } from "@/components/top-bar"
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
import { EnvBadge } from "@/mock/components/bits"
import { LoomThread } from "@/mock/components/loom"
import { duration } from "@/mock/format"
import { to } from "@/mock/router"
import { useShell } from "@/mock/shell"
import { isLive, useStore } from "@/mock/store"

export type Crumb = { label: ReactNode; path?: string }

/** TopBar sits above every page: where you are, what's moving, search, deploy. */
export function TopBar({ crumbs }: { crumbs: Crumb[] }) {
  const { openCommand, openDeploy } = useShell()
  return (
    <SharedTopBar
      crumbs={crumbs.map((c) => ({
        label: c.label,
        link: c.path ? <a href={to(c.path)} /> : undefined,
      }))}
      onSearch={openCommand}
      before={<InFlight />}
      after={
        <Button size="sm" className="rounded-full" onClick={() => openDeploy()}>
          <PlusIcon />
          <span className="hidden sm:inline">New deployment</span>
          <span className="sm:hidden">Deploy</span>
        </Button>
      }
    />
  )
}

function InFlight() {
  const { deployments } = useStore()
  const live = deployments.filter(isLive)
  if (live.length === 0) return null
  const waiting = live.filter((d) => d.status === "waiting").length
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
          {live.length} in flight
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
          {live.map((d) => (
            <Item
              key={d.id}
              size="xs"
              className="flex-col items-stretch gap-1.5"
              render={<a href={to(`/deployments/${d.id}`)} />}
            >
              <ItemTitle className="w-full">
                {d.project}
                <EnvBadge env={d.env} />
                <span className="ml-auto text-xs font-normal text-muted-foreground tabular-nums">
                  {duration(d.elapsedMs)}
                </span>
              </ItemTitle>
              <LoomThread steps={d.steps} />
              <ItemDescription className="text-xs">
                {d.status === "waiting"
                  ? "Waiting for approval"
                  : d.steps[d.current]?.step.name}
              </ItemDescription>
            </Item>
          ))}
        </ItemGroup>
      </PopoverContent>
    </Popover>
  )
}
