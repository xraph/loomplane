// The bar above every page of the dashboard: the shared top bar, with the
// organization first in the breadcrumbs and search opening the command menu.
import { TopBar } from "@/components/top-bar"
import { useShell } from "@/components/shell"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

export type Crumb = {
  label: string
  to?: "/" | "/infrastructure" | "/members" | "/integrations" | "/variables"
}

export function SiteHeader({ crumbs }: { crumbs: Crumb[] }) {
  const { openCommand } = useShell()
  const { session, organizations } = useOrganization()
  const org = organizations.find((o) => o.id === session.orgId)
  return (
    <TopBar
      onSearch={openCommand}
      crumbs={[
        ...(org ? [{ label: org.name, link: <Link to="/" /> }] : []),
        ...crumbs.map((c) => ({
          label: c.label,
          link: c.to ? <Link to={c.to} /> : undefined,
        })),
      ]}
    />
  )
}
