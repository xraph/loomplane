"use client"

import * as React from "react"
import { cn } from "cn"

type TableProps = React.ComponentProps<"table"> & {
  /** Fixed grid tracks shared by every header and data row. */
  columns?: string
  /** Names the keyboard-focusable horizontal scroll area. */
  scrollLabel?: string
}

function Table({
  className,
  columns,
  scrollLabel,
  style,
  ...props
}: TableProps) {
  return (
    <div
      data-slot="table-container"
      role={scrollLabel ? "region" : undefined}
      aria-label={scrollLabel}
      tabIndex={scrollLabel ? 0 : undefined}
      className="relative w-full overflow-x-auto focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
    >
      <table
        data-slot="table"
        role="table"
        className={cn(
          "w-full caption-bottom text-[13px]",
          columns &&
            "block [&_tbody_tr:hover]:bg-surface-muted [&_td]:min-w-0 [&_td]:p-0 [&_td]:[overflow-wrap:anywhere] [&_td]:whitespace-normal [&_th]:h-auto [&_th]:p-0 [&_thead_tr]:py-[9px] [&_tr]:grid [&_tr]:grid-cols-(--table-columns) [&_tr]:items-center [&_tr]:gap-3 [&_tr]:border-border-divider [&_tr]:px-4 [&_tr]:py-2.5 [&>tbody]:block [&>thead]:block",
          className
        )}
        style={{ "--table-columns": columns, ...style } as React.CSSProperties}
        {...props}
      />
    </div>
  )
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  return (
    <thead
      data-slot="table-header"
      role="rowgroup"
      className={cn("[&_tr]:border-b", className)}
      {...props}
    />
  )
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
  return (
    <tbody
      data-slot="table-body"
      role="rowgroup"
      className={cn("[&_tr:last-child]:border-0", className)}
      {...props}
    />
  )
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
  return (
    <tfoot
      data-slot="table-footer"
      role="rowgroup"
      className={cn(
        "border-t bg-muted/50 font-medium [&>tr]:last:border-b-0",
        className
      )}
      {...props}
    />
  )
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      role="row"
      className={cn(
        "border-b transition-colors hover:bg-muted/50 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-muted",
        className
      )}
      {...props}
    />
  )
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head"
      role="columnheader"
      scope="col"
      className={cn(
        "h-10 px-2 text-left align-middle font-medium whitespace-nowrap text-foreground [&:has([role=checkbox])]:pr-0",
        className
      )}
      {...props}
    />
  )
}

function TableCell({ className, ...props }: React.ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      role="cell"
      className={cn(
        "p-2 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0",
        className
      )}
      {...props}
    />
  )
}

function TableCaption({
  className,
  ...props
}: React.ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("mt-4 text-[13px] text-muted-foreground", className)}
      {...props}
    />
  )
}

export {
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
}
