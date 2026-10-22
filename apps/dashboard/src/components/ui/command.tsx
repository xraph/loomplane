"use client"

import * as React from "react"
import { Command as CommandPrimitive } from "cmdk"
import { cn } from "cn"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { SearchIcon, CheckIcon } from "lucide-react"

function Command({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive>) {
  return (
    <CommandPrimitive
      data-slot="command"
      className={cn(
        "flex size-full flex-col overflow-hidden rounded-xl! bg-popover p-1 text-popover-foreground",
        className
      )}
      {...props}
    />
  )
}

function CommandDialog({
  title = "Command Palette",
  description = "Search for a command to run...",
  children,
  className,
  showCloseButton = false,
  ...props
}: Omit<React.ComponentProps<typeof Dialog>, "children"> & {
  title?: string
  description?: string
  className?: string
  showCloseButton?: boolean
  children: React.ReactNode
}) {
  return (
    <Dialog {...props}>
      <DialogHeader className="sr-only">
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <DialogContent
        className={cn(
          "top-[min(120px,12vh)] translate-y-0 gap-0 overflow-hidden rounded-2xl! p-0 shadow-[0_0_0_1px_oklch(0.2_0.01_50/0.08),0_30px_80px_oklch(0.2_0.01_50/0.28)] ring-0 sm:max-w-[640px]",
          className
        )}
        showCloseButton={showCloseButton}
      >
        {children}
      </DialogContent>
    </Dialog>
  )
}

function CommandInput({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Input>) {
  return (
    <div
      data-slot="command-input-wrapper"
      className="flex h-14 shrink-0 items-center gap-2.5 border-b px-4"
    >
      <SearchIcon className="size-[18px] shrink-0 text-muted-foreground" />
      <CommandPrimitive.Input
        data-slot="command-input"
        className={cn(
          "h-full min-w-0 flex-1 bg-transparent text-[15px] outline-hidden placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
          className
        )}
        {...props}
      />
      <kbd className="rounded-[5px] border px-1.5 py-px font-mono text-[11px] text-muted-foreground">
        esc
      </kbd>
    </div>
  )
}

function CommandList({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.List>) {
  return (
    <CommandPrimitive.List
      data-slot="command-list"
      className={cn(
        "no-scrollbar max-h-[min(480px,60vh)] scroll-py-2 overflow-x-hidden overflow-y-auto px-2 pt-1.5 pb-2.5 outline-none",
        className
      )}
      {...props}
    />
  )
}

function CommandEmpty({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Empty>) {
  return (
    <CommandPrimitive.Empty
      data-slot="command-empty"
      className={cn(
        "py-9 text-center text-[13.5px] text-muted-foreground",
        className
      )}
      {...props}
    />
  )
}

function CommandGroup({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Group>) {
  return (
    <CommandPrimitive.Group
      data-slot="command-group"
      className={cn(
        "overflow-hidden pt-1.5 text-foreground **:[[cmdk-group-heading]]:px-2.5 **:[[cmdk-group-heading]]:pt-1.5 **:[[cmdk-group-heading]]:pb-1 **:[[cmdk-group-heading]]:text-[11.5px] **:[[cmdk-group-heading]]:font-semibold **:[[cmdk-group-heading]]:tracking-[0.05em] **:[[cmdk-group-heading]]:text-muted-foreground **:[[cmdk-group-heading]]:uppercase",
        className
      )}
      {...props}
    />
  )
}

function CommandSeparator({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Separator>) {
  return (
    <CommandPrimitive.Separator
      data-slot="command-separator"
      className={cn("-mx-1 h-px bg-border", className)}
      {...props}
    />
  )
}

function CommandItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Item>) {
  return (
    <CommandPrimitive.Item
      data-slot="command-item"
      className={cn(
        "group/command-item relative flex cursor-default items-center gap-3 rounded-[10px] px-2.5 py-2 text-[13.5px] outline-hidden select-none in-data-[slot=dialog-content]:rounded-lg! data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-muted data-selected:text-foreground [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 data-selected:*:[svg]:text-foreground",
        className
      )}
      {...props}
    >
      {children}
      <CheckIcon className="ml-auto hidden group-data-[checked=true]/command-item:block" />
    </CommandPrimitive.Item>
  )
}

function CommandShortcut({
  className,
  children,
  ...props
}: React.ComponentProps<"span">) {
  // "G P" reads as two keys pressed in turn, each in its own cap.
  const keys =
    typeof children === "string" ? children.split(" ").filter(Boolean) : null
  return (
    <span
      data-slot="command-shortcut"
      className={cn(
        "ml-auto flex shrink-0 gap-[3px] text-muted-foreground",
        className
      )}
      {...props}
    >
      {keys
        ? keys.map((k, n) => (
            <kbd
              key={n}
              className="min-w-[18px] rounded-[5px] border bg-background px-1.5 py-px text-center font-mono text-[11px]"
            >
              {k}
            </kbd>
          ))
        : children}
    </span>
  )
}

export {
  Command,
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandShortcut,
  CommandSeparator,
}
