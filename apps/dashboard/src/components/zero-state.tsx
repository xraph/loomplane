import { cva, type VariantProps } from "class-variance-authority"
import {
  Children,
  createContext,
  isValidElement,
  type ReactNode,
  useContext,
} from "react"
import { EmptyIcon } from "@/components/icons"
import { cn } from "@/lib/utils"

type Variant = "panel" | "inline" | "compact"

// The header draws the preview on the page and only the icon inside a card.
const VariantContext = createContext<{ variant: Variant; preview: boolean }>({
  variant: "panel",
  preview: true,
})

const zeroStateVariants = cva(
  "flex w-full min-w-0 flex-col justify-center gap-5",
  {
    variants: {
      variant: {
        // On the page: centred under a faded preview of the list that will
        // fill this space, on a soft surface that sets it apart from the
        // sections around it. Never a border.
        panel: "items-center rounded-[14px] px-6 py-10 text-center",
        // Already inside a card, so just the content.
        inline: "items-start p-5 text-left",
        compact: "items-start gap-3 py-5 text-left",
      },
    },
    defaultVariants: { variant: "panel" },
  }
)

function ZeroState({
  className,
  variant = "panel",
  preview = true,
  surface = true,
  ...props
}: React.ComponentProps<"div"> &
  VariantProps<typeof zeroStateVariants> & {
    /**
     * Draw the faded rows of what will fill this space. Turn it off for an
     * error, a missing permission or a page that isn't there.
     */
    preview?: boolean
    /**
     * The soft surface behind a panel. Turn it off when the panel already
     * sits inside a bordered card, so there's no box inside a box.
     */
    surface?: boolean
  }) {
  const v = variant ?? "panel"
  return (
    <VariantContext.Provider value={{ variant: v, preview }}>
      <div
        data-slot="empty"
        data-variant={v}
        data-preview={preview}
        data-surface={v === "panel" && surface}
        className={cn(
          zeroStateVariants({ variant: v }),
          v === "panel" && surface && "bg-subtle",
          className
        )}
        {...props}
      />
    </VariantContext.Provider>
  )
}

function ZeroStateHeader({
  className,
  children,
  ...props
}: React.ComponentProps<"div">) {
  const { variant, preview } = useContext(VariantContext)
  const items = Children.toArray(children)
  const media = items.find(
    (child) => isValidElement(child) && child.type === ZeroStateMedia
  ) ?? (
    <ZeroStateMedia variant="icon">
      <EmptyIcon />
    </ZeroStateMedia>
  )
  const rest = items.filter((child) => child !== media)
  return (
    <div
      data-slot="empty-header"
      className={cn(
        "flex max-w-md flex-col gap-1.5",
        variant === "panel" ? "items-center" : "items-start",
        className
      )}
      {...props}
    >
      {(variant === "panel" || variant === "compact") && preview ? (
        <Preview>{media}</Preview>
      ) : (
        media
      )}
      {rest}
    </div>
  )
}

// Three placeholder rows, fading out, with the state's icon leading the
// first: where the first item will show up.
function Preview({ children }: { children: ReactNode }) {
  return (
    <div
      aria-hidden="true"
      className="mb-1 grid w-[min(320px,100%)] gap-2 [mask-image:linear-gradient(to_bottom,black_30%,transparent)]"
    >
      <GhostRow lead={children} />
      <GhostRow className="scale-[0.95] opacity-70" />
      <GhostRow className="scale-[0.9] opacity-40" />
    </div>
  )
}

function GhostRow({
  lead,
  className,
}: {
  lead?: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex h-12 items-center gap-3 rounded-xl bg-card px-2.5 shadow-[0_1px_2px_oklch(0.2_0.01_50/0.06)] in-data-[surface=false]:bg-muted/60 in-data-[surface=false]:shadow-none",
        className
      )}
    >
      {lead ?? <span className="size-7 shrink-0 rounded-lg bg-muted" />}
      <span className="grid flex-1 gap-1.5">
        <span className="h-2 w-3/5 rounded-full bg-muted-foreground/20" />
        <span className="h-1.5 w-2/5 rounded-full bg-muted-foreground/12" />
      </span>
      <span className="h-4 w-10 shrink-0 rounded-md bg-muted" />
    </div>
  )
}

const emptyMediaVariants = cva(
  "flex shrink-0 items-center justify-center [&_svg]:pointer-events-none [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-transparent",
        icon: "size-7 rounded-lg bg-brand-tint text-brand-ink in-data-[preview=false]:mb-3 in-data-[preview=false]:size-11 in-data-[preview=false]:rounded-xl in-data-[variant=inline]:mb-2 in-data-[variant=inline]:size-9 in-data-[variant=inline]:rounded-[10px] [&_svg:not([class*='size-'])]:size-4 in-data-[preview=false]:[&_svg:not([class*='size-'])]:size-5",
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
        "font-heading text-[15px] font-semibold tracking-[-0.01em]",
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
        "max-w-sm text-[13px]/relaxed text-pretty text-muted-foreground [&>a]:underline [&>a]:underline-offset-4 [&>a:hover]:text-primary",
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
        "flex w-full max-w-sm min-w-0 flex-wrap items-center gap-2 text-[13px] in-data-[variant=panel]:justify-center",
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
