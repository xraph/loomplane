// A product's own mark, such as PostgreSQL's elephant or Vercel's
// triangle, on a white tile. Anything without a mark gets a plain icon.
import type { ComponentType, CSSProperties } from "react"

import { productLogos, type ProductKey } from "@/components/product-logos"
import { cn } from "@/lib/utils"

// icon sizes a fallback that reads only its className, as mark does
// through style for one that takes both.
const sizes = {
  xs: { tile: "size-5 rounded-[6px]", mark: 12, icon: "size-3" },
  sm: { tile: "size-7 rounded-lg", mark: 15, icon: "size-[15px]" },
  md: { tile: "size-8 rounded-[9px]", mark: 17, icon: "size-[17px]" },
  lg: { tile: "size-10 rounded-[11px]", mark: 20, icon: "size-5" },
  xl: { tile: "size-11 rounded-xl", mark: 22, icon: "size-[22px]" },
} as const

export type ProductIconSize = keyof typeof sizes

// Near-black marks follow the text color so they still show in dark mode.
function isDark(hex: string) {
  const n = Number.parseInt(hex.slice(1), 16)
  const lum =
    (0.2126 * ((n >> 16) & 255) +
      0.7152 * ((n >> 8) & 255) +
      0.0722 * (n & 255)) /
    255
  return lum < 0.25
}

export function ProductMark({
  product,
  size = 16,
  className,
}: {
  product: ProductKey
  size?: number
  className?: string
}) {
  const logo = productLogos[product]
  const dark = isDark(logo.color)
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      aria-hidden="true"
      className={cn("shrink-0", dark && "text-foreground", className)}
      style={dark ? undefined : { color: logo.color }}
    >
      <path d={logo.path} fill="currentColor" />
    </svg>
  )
}

export function ProductIcon({
  product,
  fallback: Fallback,
  label,
  size = "md",
  className,
}: {
  product?: ProductKey
  /**
   * Drawn when there's no product mark: a Lucide icon, or any icon that
   * takes a className.
   */
  fallback?: ComponentType<{
    className?: string
    style?: CSSProperties
    "aria-hidden"?: boolean | "true" | "false"
  }>
  /** The accessible name; defaults to the product's name. */
  label?: string
  size?: ProductIconSize
  className?: string
}) {
  const s = sizes[size]
  const name = label ?? (product ? productLogos[product].title : undefined)
  return (
    <span
      title={name}
      className={cn(
        "inline-flex shrink-0 items-center justify-center bg-background text-foreground ring-1 ring-border",
        s.tile,
        className
      )}
    >
      {product ? (
        <ProductMark product={product} size={s.mark} />
      ) : Fallback ? (
        <Fallback
          aria-hidden="true"
          className={cn("shrink-0", s.icon)}
          style={{ width: s.mark, height: s.mark }}
        />
      ) : null}
      {name && <span className="sr-only">{name}</span>}
    </span>
  )
}
