import type { ReactNode } from "react"

import { BrandMark } from "@/components/brand-mark"
import { cn } from "@/lib/utils"

/**
 * OnboardingLayout is the frame for every signed-in page outside the
 * dashboard: creating a workspace, joining one, and the states the shell
 * can't show, like a workspace address that opens nothing. It's the mock's
 * workspace onboarding design: a soft ground with the mark faded in a
 * corner, the brand above with who's signed in, and one card in the middle.
 * Signing in and recovering an account keep AuthLayout.
 *
 * It uses no router, theme or client hooks, so it renders even when the
 * dashboard couldn't start.
 */
export function OnboardingLayout({
  title,
  lede,
  tone = "neutral",
  account,
  onSignOut,
  children,
  footer,
}: {
  title: string
  lede: ReactNode
  /** problem marks the title as something that went wrong. */
  tone?: "neutral" | "problem"
  /** Who's signed in, shown at the top right. */
  account?: string
  /** Shown beside the account, so signing out never sits in the card. */
  onSignOut?: () => void
  children?: ReactNode
  /** Links at the foot of the card, such as the way back to a workspace. */
  footer?: ReactNode
}) {
  return (
    <div className="relative flex min-h-svh flex-col overflow-hidden bg-muted bg-[radial-gradient(70%_60%_at_15%_10%,var(--brand-tint)_0%,transparent_60%),radial-gradient(60%_60%_at_90%_90%,var(--flight-tint)_0%,transparent_60%)] text-foreground antialiased">
      <svg
        viewBox="0 0 32 32"
        aria-hidden="true"
        className="pointer-events-none absolute -right-44 -bottom-56 size-[760px] fill-foreground opacity-[0.04]"
      >
        <path d="M0 10.5L0 21A11 11 0 0 0 11 32L28 32A4 4 0 0 0 28 24L11 24A3 3 0 0 1 8 21L8 10.5Z" />
        <path d="M32 21.5L32 11A11 11 0 0 0 21 0L4 0A4 4 0 0 0 4 8L21 8A3 3 0 0 1 24 11L24 21.5Z" />
      </svg>
      <header className="relative flex items-center justify-between gap-3 px-6 py-[18px]">
        <span className="flex items-center gap-2.5">
          <span className="flex size-8 items-center justify-center rounded-[9px] bg-linear-145 from-brand-from via-brand-via to-brand-to shadow-[inset_0_1px_0_oklch(1_0_0/0.25)]">
            <BrandMark inverse className="size-[17px] text-white" />
          </span>
          <span className="text-[15px] font-semibold tracking-tight">
            Loomplane
          </span>
        </span>
        {(account || onSignOut) && (
          <span className="flex min-w-0 items-center gap-3 text-[12.5px] text-muted-foreground">
            {account && <span className="truncate">{account}</span>}
            {onSignOut && (
              <button
                type="button"
                onClick={onSignOut}
                className="h-8 flex-none rounded-[10px] bg-card/70 px-3 font-medium text-foreground shadow-[0_0_0_1px_rgba(26,22,20,0.06)] hover:bg-card"
              >
                Sign out
              </button>
            )}
          </span>
        )}
      </header>
      <main className="relative flex flex-1 items-center justify-center px-4 pt-4 pb-10">
        <section
          role={tone === "problem" ? "alert" : undefined}
          className="flex w-full max-w-[520px] flex-col gap-5 rounded-[18px] bg-card p-7 shadow-[0_0_0_1px_rgba(26,22,20,0.06),0_18px_50px_rgba(26,22,20,0.08)]"
        >
          <div className="grid gap-1.5">
            <h1
              className={cn(
                "text-2xl font-semibold tracking-tight",
                tone === "problem" && "text-failed"
              )}
            >
              {title}
            </h1>
            <p className="text-[13px] leading-relaxed text-muted-foreground">
              {lede}
            </p>
          </div>
          {children}
          {footer && (
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 text-[13px] text-muted-foreground">
              {footer}
            </div>
          )}
        </section>
      </main>
      <footer className="relative px-6 pb-5 text-center text-xs text-muted-foreground">
        Loomplane by Xraph
      </footer>
    </div>
  )
}
