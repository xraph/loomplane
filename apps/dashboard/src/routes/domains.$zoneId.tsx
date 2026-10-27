import { createFileRoute, Link } from "@tanstack/react-router"

import { Page, PageHeading } from "@/components/page"

export const Route = createFileRoute("/domains/$zoneId")({
  component: DomainRoute,
})

// A domain's own page. Until its sections land it names the zone and
// leads back to the list.
function DomainRoute() {
  const { zoneId } = Route.useParams()
  return (
    <Page className="max-w-[1180px] gap-4">
      <PageHeading title={zoneId} />
      <Link to="/domains" className="text-[13px] underline underline-offset-4">
        Back to Domains
      </Link>
    </Page>
  )
}
