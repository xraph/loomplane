import { ChevronLeftIcon, EllipsisIcon, TriangleAlertIcon } from "lucide-react"
import {
  useState,
  type ComponentType,
  type CSSProperties,
  type ReactNode,
} from "react"

import { Button } from "@/components/ui/button"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

// The standard parts of a Cloud page. Every detail page (a Cloud, a cluster,
// a connected account) stacks the same header, section heads and tables, so
// a person learns the layout once. Button sizes follow the level a button
// sits at: default in the page header and dialog footers, sm in section
// heads and rows. Each tab has at most one primary button, on its main
// section's create action.

/**
 * CloudPageHeader is a detail page's header: a glyph tile, the title, one
 * status, one meta line, and the page's actions. A child page names its
 * parent above the title; the breadcrumb is the other way back.
 */
export function CloudPageHeader({
  glyph,
  title,
  status,
  meta,
  actions,
  parent,
  mono,
}: {
  glyph: ReactNode
  title: ReactNode
  status?: ReactNode
  meta?: ReactNode
  /** At most two outline buttons, then a PageMenu. */
  actions?: ReactNode
  parent?: { label: string; onClick: () => void }
  /** Titles that are machine names, like a cluster's, read in mono. */
  mono?: boolean
}) {
  return (
    <header className="grid gap-3">
      {parent && (
        <button
          type="button"
          onClick={parent.onClick}
          className="inline-flex items-center gap-1.5 self-start justify-self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" aria-hidden="true" />
          {parent.label}
        </button>
      )}
      <div className="flex flex-wrap items-center justify-between gap-3.5">
        <div className="flex min-w-0 items-center gap-3.5">
          <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-muted">
            {glyph}
          </span>
          <div className="grid min-w-0 gap-1">
            <div className="flex flex-wrap items-center gap-2.5">
              <h1
                className={cn(
                  "leading-tight break-words",
                  mono
                    ? "font-mono text-[22px] font-medium tracking-[-0.02em]"
                    : "text-2xl font-semibold tracking-[-0.025em]"
                )}
              >
                {title}
              </h1>
              {status}
            </div>
            {meta && (
              <p className="text-[13px] text-muted-foreground">{meta}</p>
            )}
          </div>
        </div>
        {actions && (
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {actions}
          </div>
        )}
      </div>
    </header>
  )
}

/** PageMenu is the "More" menu at the end of a page header's actions. */
export function PageMenu({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="outline" size="icon" aria-label={label} />}
      >
        <EllipsisIcon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * RowMenu holds a row's less common actions, after the one visible button.
 * Destructive items go last, as DropdownMenuItem variant="destructive".
 */
export function RowMenu({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground"
            aria-label={label}
          />
        }
      >
        <EllipsisIcon />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * CloudTable is every list on a Cloud page: a bordered box, a column header
 * and divided rows that scroll sideways on a narrow screen. columns is the
 * CSS grid template the header and each row share.
 */
export function CloudTable({
  label,
  columns,
  head,
  children,
  footer,
  minWidth = 640,
}: {
  label: string
  columns: string
  /** Column names; an empty string leaves a column (actions) unnamed. */
  head?: string[]
  children: ReactNode
  footer?: ReactNode
  minWidth?: number
}) {
  return (
    <div className="overflow-x-auto rounded-xl bg-card ring-1 ring-border">
      <div
        role="table"
        aria-label={label}
        style={{ "--cloud-cols": columns, minWidth } as CSSProperties}
      >
        {head && (
          <div
            role="row"
            className="grid grid-cols-(--cloud-cols) gap-3.5 border-b bg-muted/40 px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase"
          >
            {head.map((h, i) => (
              <span
                key={i}
                role="columnheader"
                className={cn(i === head.length - 1 && !h && "sr-only")}
              >
                {h || "Actions"}
              </span>
            ))}
          </div>
        )}
        <div role="rowgroup">{children}</div>
        {footer && (
          <div className="flex flex-wrap items-center justify-between gap-2 border-t bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
            {footer}
          </div>
        )}
      </div>
    </div>
  )
}

/** CloudTableRow is one row of a CloudTable; each child is a cell. */
export function CloudTableRow({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <div
      role="row"
      className={cn(
        "grid grid-cols-(--cloud-cols) items-center gap-3.5 px-4 py-2.5 text-[13px] transition-colors not-last:border-b hover:bg-muted/30 motion-reduce:transition-none",
        className
      )}
    >
      {children}
    </div>
  )
}

/** Cell is a table cell; its content may wrap but never widens the row. */
export function Cell({
  children,
  className,
}: {
  children?: ReactNode
  className?: string
}) {
  return (
    <div role="cell" className={cn("min-w-0", className)}>
      {children}
    </div>
  )
}

/** ActionsCell right-aligns a row's visible button and its RowMenu. */
export function ActionsCell({ children }: { children?: ReactNode }) {
  return (
    <div role="cell" className="flex items-center justify-end gap-1">
      {children}
    </div>
  )
}

/**
 * NameCell is a row's first cell: a tile, the name (a link when the row
 * opens something) and one meta line.
 */
export function NameCell({
  tile,
  name,
  meta,
  mono,
}: {
  tile?: ReactNode
  name: ReactNode
  meta?: ReactNode
  mono?: boolean
}) {
  return (
    <div role="cell" className="flex min-w-0 items-center gap-2.5">
      {tile && (
        <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground [&_svg]:size-3.5">
          {tile}
        </span>
      )}
      <span className="grid min-w-0 gap-px">
        <span
          className={cn(
            "truncate font-semibold",
            mono && "font-mono font-medium"
          )}
        >
          {name}
        </span>
        {meta && (
          <span className="truncate text-xs text-muted-foreground">{meta}</span>
        )}
      </span>
    </div>
  )
}

/** FactList is a section's key-value facts in the table's box. */
export function FactList({
  facts,
}: {
  facts: { label: string; value: ReactNode }[]
}) {
  return (
    <dl className="rounded-xl bg-card px-4 py-0.5 ring-1 ring-border">
      {facts.map((f) => (
        <div
          key={f.label}
          className="flex justify-between gap-3 py-2.5 not-last:border-b"
        >
          <dt className="text-[12.5px] text-muted-foreground">{f.label}</dt>
          <dd className="min-w-0 text-right text-[13px] break-words tabular-nums">
            {f.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

const calloutTone = {
  attention: {
    box: "bg-weld-tint/60 ring-weld/30",
    icon: "text-weld-ink",
    title: "",
  },
  info: { box: "bg-muted ring-border", icon: "text-foreground", title: "" },
  error: {
    box: "bg-failed-tint ring-destructive/30",
    icon: "text-destructive",
    title: "text-destructive",
  },
}

/**
 * CloudCallout is a notice above a tab's sections: attention when someone
 * must act, info when a setting changes how the page behaves, error when a
 * read failed.
 */
export function CloudCallout({
  tone = "attention",
  title,
  children,
  action,
  icon: Icon = TriangleAlertIcon,
}: {
  tone?: keyof typeof calloutTone
  title: ReactNode
  children?: ReactNode
  action?: ReactNode
  icon?: ComponentType<{ className?: string }>
}) {
  const t = calloutTone[tone]
  return (
    <div
      role={tone === "error" ? "alert" : undefined}
      className={cn(
        "flex flex-wrap items-center gap-x-3.5 gap-y-2.5 rounded-xl py-3 pr-3.5 pl-4 ring-1",
        t.box
      )}
    >
      <Icon className={cn("size-4 shrink-0", t.icon)} aria-hidden="true" />
      <div className="grid min-w-0 flex-[1_1_320px] gap-0.5">
        <p className={cn("text-[13px] font-semibold", t.title)}>{title}</p>
        {children && (
          <div className="text-[12.5px] leading-relaxed text-muted-foreground">
            {children}
          </div>
        )}
      </div>
      {action}
    </div>
  )
}

/**
 * Segments switch between lists inside one tab, like Infrastructure's
 * Servers, Pools and Clusters.
 */
export function Segments<T extends string>({
  label,
  value,
  onChange,
  items,
}: {
  label: string
  value: T
  onChange: (value: T) => void
  items: { id: T; label: string; count?: number }[]
}) {
  return (
    <div
      role="tablist"
      aria-label={label}
      className="inline-flex flex-wrap gap-0.5 self-start justify-self-start rounded-[10px] bg-muted p-[3px]"
    >
      {items.map((item) => {
        const on = item.id === value
        return (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={on}
            onClick={() => onChange(item.id)}
            className={cn(
              "flex h-7 items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
              on
                ? "bg-background text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            {item.label}
            {item.count !== undefined && (
              <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                {item.count}
              </span>
            )}
          </button>
        )
      })}
    </div>
  )
}

/**
 * CloudConfirm asks before something is deleted, removed or stopped. It
 * says what goes and what stays; deleting a named thing asks for its name.
 */
export function CloudConfirm({
  open,
  onOpenChange,
  title,
  children,
  confirmLabel,
  typeToConfirm,
  onConfirm,
  pending,
  error,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  children: ReactNode
  confirmLabel: string
  /** The name a person types to confirm a delete. */
  typeToConfirm?: string
  onConfirm: () => void
  pending?: boolean
  error?: ReactNode
}) {
  const [typed, setTyped] = useState("")
  const locked = !!typeToConfirm && typed !== typeToConfirm
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setTyped("")
        onOpenChange(next)
      }}
    >
      <AlertDialogContent className="data-[size=default]:sm:max-w-md">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{children}</AlertDialogDescription>
        </AlertDialogHeader>
        {typeToConfirm && (
          <label className="grid gap-1.5 text-[12.5px] text-muted-foreground">
            <span>
              Type{" "}
              <span className="font-mono text-foreground">{typeToConfirm}</span>{" "}
              to confirm
            </span>
            <Input
              value={typed}
              autoComplete="off"
              className="font-mono"
              onChange={(e) => setTyped(e.target.value)}
            />
          </label>
        )}
        {error}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <Button
            variant="destructive"
            disabled={locked || pending}
            onClick={onConfirm}
          >
            {pending && <Spinner />}
            {confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

const hoursPerMonth = 730

const dollars = (usd: number) =>
  usd >= 1 || usd === 0
    ? `$${usd.toFixed(2)}`
    : `$${usd.toFixed(usd >= 0.1 ? 2 : 3)}`

/**
 * hourlyCost is what something costs, written one way everywhere: the hourly
 * price, then roughly what that comes to in a month.
 */
export function hourlyCost(usdPerHour: number): string {
  return `${dollars(usdPerHour)}/hr · about $${Math.round(
    usdPerHour * hoursPerMonth
  ).toLocaleString("en-US")}/mo`
}

/** monthlyCost is a rough monthly price from an hourly one. */
export function monthlyCost(usdPerHour: number): string {
  return `about $${Math.round(usdPerHour * hoursPerMonth).toLocaleString("en-US")}/mo`
}

/** ChildLink opens a record's own page from a row's name. */
export function ChildLink({
  children,
  ...props
}: React.ComponentProps<typeof Link>) {
  return (
    <Link
      {...props}
      className="text-foreground underline-offset-2 hover:underline"
    >
      {children}
    </Link>
  )
}
