import { cva, type VariantProps } from "class-variance-authority"
import { Children, isValidElement } from "react"
import { LayersIcon } from "lucide-react"
import { cn } from "@/lib/utils"

function ZeroState({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="empty"
      className={cn(
        "flex w-full min-w-0 flex-1 flex-col items-start justify-center gap-3 rounded-xl border-dashed p-5 text-left",
        className
      )}
      {...props}
    />
  )
}

function ZeroStateHeader({
  className,
  children,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="empty-header"
      className={cn("flex max-w-md flex-col items-start gap-2", className)}
      {...props}
    >
      {!Children.toArray(children).some(
        (child) => isValidElement(child) && child.type === ZeroStateMedia
      ) && (
        <ZeroStateMedia variant="icon">
          <LayersIcon />
        </ZeroStateMedia>
      )}
      {children}
    </div>
  )
}

const emptyMediaVariants = cva(
  "mb-1 flex shrink-0 items-center justify-center [&_svg]:pointer-events-none [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-transparent",
        icon: "canvas-dots flex h-14 w-20 shrink-0 items-center justify-center rounded-lg border bg-muted/40 text-muted-foreground [&_svg:not([class*='size-'])]:size-6",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function ZeroStateMedia({
  className,
  variant = "default",
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof emptyMediaVariants>) {
  return (
    <div
      data-slot="empty-icon"
      data-variant={variant}
      className={cn(emptyMediaVariants({ variant, className }))}
      {...props}
    />
  )
}

function ZeroStateTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="empty-title"
      className={cn(
        "font-heading text-sm font-medium tracking-tight",
        className
      )}
      {...props}
    />
  )
}

function ZeroStateDescription({
  className,
  ...props
}: React.ComponentProps<"p">) {
  return (
    <div
      data-slot="empty-description"
      className={cn(
        "text-sm/relaxed text-muted-foreground [&>a]:underline [&>a]:underline-offset-4 [&>a:hover]:text-primary",
        className
      )}
      {...props}
    />
  )
}

function ZeroStateContent({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="empty-content"
      className={cn(
        "flex w-full max-w-sm min-w-0 flex-col items-start gap-2 text-sm",
        className
      )}
      {...props}
    />
  )
}

export {
  ZeroState,
  ZeroStateHeader,
  ZeroStateTitle,
  ZeroStateDescription,
  ZeroStateContent,
  ZeroStateMedia,
}
