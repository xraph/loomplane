// A cloud's tile: the provider's own mark where it has one, a plain icon
// for SSH servers and registries.
import { PackageIcon, ServerIcon } from "lucide-react"

import { ProductIcon, type ProductIconSize } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { cloudLabel } from "@/lib/clouds"

const products: Record<string, ProductKey> = {
  vercel: "vercel",
  aws: "aws",
  gcp: "gcp",
  render: "render",
  digitalocean: "digitalocean",
  cloudflare: "cloudflare",
  kubernetes: "kubernetes",
}

export function cloudProduct(cloud: string): ProductKey | undefined {
  return products[cloud]
}

export function CloudGlyph({
  cloud,
  size = "xs",
  className,
}: {
  cloud: string
  size?: ProductIconSize
  className?: string
}) {
  return (
    <ProductIcon
      product={cloudProduct(cloud)}
      fallback={cloud === "registry" ? PackageIcon : ServerIcon}
      label={cloudLabel(cloud)}
      size={size}
      className={className}
    />
  )
}
