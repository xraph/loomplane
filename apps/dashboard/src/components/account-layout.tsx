// The frame every account page shares: its heading and the tabs between
// Profile, Security and Sessions. They're your own settings, not the
// workspace's, so they live apart from the Settings menu.
import type { ReactNode } from "react"

import { Page, PageHeading } from "@/components/page"
import { SiteHeader } from "@/components/site-header"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

export type AccountTab = "profile" | "security" | "sessions"

const tabs = [
  { id: "profile", label: "Profile", to: "/account/profile" },
  { id: "security", label: "Security", to: "/account/security" },
  { id: "sessions", label: "Sessions", to: "/account/sessions" },
] as const

export function AccountLayout({
  tab,
  children,
}: {
  tab: AccountTab
  children: ReactNode
}) {
  const current = tabs.find((t) => t.id === tab)!
  return (
    <>
      <SiteHeader
        crumbs={[{ label: "Your account" }, { label: current.label }]}
      />
      <Page className="gap-6">
        <div className="grid gap-4">
          <PageHeading
            title="Your account"
            description="These follow you into every workspace you belong to."
          />
          <nav
            aria-label="Account"
            className="flex flex-wrap gap-1 border-b border-border"
          >
            {tabs.map((t) => (
              <Link
                key={t.id}
                to={t.to}
                aria-current={t.id === tab ? "page" : undefined}
                className={cn(
                  "-mb-px border-b-2 px-2.5 py-2 text-[13.5px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  t.id === tab
                    ? "border-foreground text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground"
                )}
              >
                {t.label}
              </Link>
            ))}
          </nav>
        </div>
        {children}
      </Page>
    </>
  )
}
