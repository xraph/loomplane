import { expect, it } from "vitest"

import { decode } from "@/client/forge/loomplane/codecs"
import { ops } from "@/client/forge/loomplane/ops"
import type { DomainDetail } from "@/client/forge/loomplane/types"

it("decodes a domain detail into camelCase", () => {
  const op = ops["dnsZones.get"]
  expect(op.path).toBe("/api/v1/dns-zones/{zoneId}")

  const domain = decode<DomainDetail>(
    {
      domain: {
        state: "verified",
        wildcards: false,
        hostnames: [],
        subdomains: ["api.example.com"],
        parent_zone_id: "zone-parent",
        proof_zone_id: "zone-parent",
        zone: { id: "zone-1", name: "app.example.com" },
      },
      records: [
        {
          route_id: "route-1",
          project_name: "storefront",
          hostname: "app.example.com",
        },
      ],
    },
    op.responseCodec
  )

  expect(domain.domain.parentZoneId).toBe("zone-parent")
  expect(domain.domain.proofZoneId).toBe("zone-parent")
  expect(domain.domain.subdomains).toEqual(["api.example.com"])
  expect(domain.records[0]?.routeId).toBe("route-1")
  expect(domain.records[0]?.projectName).toBe("storefront")
})
