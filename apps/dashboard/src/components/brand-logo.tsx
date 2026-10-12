import logoDark from "@/assets/brand/loomplane-logo-dark.svg"
import logoLight from "@/assets/brand/loomplane-logo.svg"
import { cn } from "@/lib/utils"

/** Outlined brand artwork, with the matching version for each theme. */
export function BrandLogo({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex w-44", className)}>
      <img
        src={logoLight}
        alt="Loomplane"
        width={656}
        height={93}
        className="block h-auto w-full dark:hidden"
      />
      <img
        src={logoDark}
        alt="Loomplane"
        width={656}
        height={93}
        className="hidden h-auto w-full dark:block"
      />
    </span>
  )
}
