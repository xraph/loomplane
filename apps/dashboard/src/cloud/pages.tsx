// What the generic /settings/$page and /platform/$page routes show in the
// cloud edition: its pages by name, and the app's own not-found page for
// any other.
import { PageNotFound } from "@/components/route-feedback"

import { BillingPage } from "./billing-page"
import { PlatformBillingPage } from "./platform-billing-page"

export function SettingsPage({ page }: { page: string }) {
  if (page === "billing") return <BillingPage />
  return <PageNotFound />
}

export function PlatformPage({ page }: { page: string }) {
  if (page === "billing") return <PlatformBillingPage />
  return <PageNotFound />
}
