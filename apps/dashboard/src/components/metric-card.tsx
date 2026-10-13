// The overview's card parts, for pages laid out the same way: a card with
// a quiet icon-and-title header, a large figure, and the tall rounded
// quick actions.
import { EllipsisIcon, type LucideIcon } from "lucide-react"
import type { ComponentType, ReactNode } from "react"

import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"

export type MenuItem = { label: string; onSelect: () => void }

export function CardMenu({
  title,
  items,
}: {
  title: string
  items: MenuItem[]
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="-mt-1 -mr-1.5 text-muted-foreground"
            aria-label={`${title} options`}
          />
        }
      >
        <EllipsisIcon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        {items.map((m) => (
          <DropdownMenuItem key={m.label} onClick={m.onSelect}>
            {m.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * MetricCard is shadcn's Card with the overview's header: icon, title,
 * and either a menu or an action of its own.
 */
export function MetricCard({
  icon: Icon,
  title,
  menu,
  action,
  className,
  children,
}: {
  icon: LucideIcon | ComponentType<{ className?: string }>
  title: string
  menu?: MenuItem[]
  action?: ReactNode
  className?: string
  children: ReactNode
}) {
  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-[15px] font-normal text-muted-foreground">
          <Icon className="size-4" />
          {title}
        </CardTitle>
        {(action || menu) && (
          <CardAction>
            {action ?? (menu && <CardMenu title={title} items={menu} />)}
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-1 flex-col gap-4">
        {children}
      </CardContent>
    </Card>
  )
}

export function Figure({
  value,
  unit,
  note,
}: {
  value: ReactNode
  unit?: ReactNode
  note?: ReactNode
}) {
  return (
    <div className="grid gap-1.5">
      <div className="flex items-baseline gap-2">
        <span className="text-[28px] leading-none font-semibold tracking-[-0.03em] tabular-nums">
          {value}
        </span>
        {unit && <span className="text-sm text-muted-foreground">{unit}</span>}
      </div>
      {note && <span className="text-xs text-muted-foreground">{note}</span>}
    </div>
  )
}

const quickAction =
  "relative h-14 justify-center gap-3 rounded-2xl bg-background px-4 shadow-[0_6px_16px_-10px_rgb(0_0_0/0.3)] sm:w-40 sm:justify-start"

/**
 * QuickAction is a tall rounded action beside a hero card's figure. With
 * href it's a link, for one that leaves Loomplane.
 */
export function QuickAction({
  icon: Icon,
  label,
  badge,
  disabled,
  href,
  onClick,
}: {
  icon: LucideIcon | ComponentType<{ className?: string }>
  label: string
  badge?: number
  disabled?: boolean
  href?: string
  onClick?: () => void
}) {
  const body = (
    <>
      <Icon className="size-5!" />
      <span className="hidden sm:inline">{label}</span>
      <span className="sr-only sm:hidden">{label}</span>
      {badge !== undefined && (
        <Badge className="absolute -top-2 -right-2 h-5 min-w-5 justify-center bg-weld px-1 text-black tabular-nums">
          {badge}
        </Badge>
      )}
    </>
  )
  if (href && !disabled) {
    return (
      <a
        href={href}
        className={cn(buttonVariants({ variant: "outline" }), quickAction)}
      >
        {body}
      </a>
    )
  }
  return (
    <Button
      variant="outline"
      disabled={disabled}
      onClick={onClick}
      className={quickAction}
    >
      {body}
    </Button>
  )
}
