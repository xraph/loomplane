import { ArrowsSplitIcon, StackIcon } from "@phosphor-icons/react"
import type { CSSProperties, ReactNode } from "react"

import { type ProductKey, productLogos } from "@/components/product-logos"

/*
 * What Loomplane connects, grouped by type around its mark: clouds and
 * clusters above, the services it deploys below, and the languages it reads
 * and the connectors it uses on either side. Tiles float, and a glint moves
 * from one to the next.
 */

type Tile = { id: string; title: string; mark: ReactNode; color: string }

// Marks drawn in near-black would vanish on the dark panel; they glint white.
const darkMarks = new Set<ProductKey>([
  "vercel",
  "github",
  "render",
  "aws",
  "mariadb",
])

function product(key: ProductKey): Tile {
  const logo = productLogos[key]
  return {
    id: key,
    title: logo.title,
    color: darkMarks.has(key) ? "#f5f5f5" : logo.color,
    mark: (
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d={logo.path} fill="currentColor" />
      </svg>
    ),
  }
}

const groups: { label: string; area: string; tiles: Tile[] }[] = [
  {
    label: "Clouds and clusters",
    area: "clouds",
    tiles: (
      ["vercel", "gcp", "aws", "render", "digitalocean", "kubernetes"] as const
    ).map(product),
  },
  {
    label: "Languages",
    area: "langs",
    tiles: (["go", "nodejs"] as const).map(product),
  },
  {
    label: "Connectors",
    area: "conn",
    tiles: (["github", "docker"] as const).map(product),
  },
  {
    label: "Services",
    area: "services",
    tiles: [
      ...(["postgresql", "mysql", "mariadb", "redis"] as const).map(product),
      {
        id: "octopus",
        title: "Octopus",
        color: "#5aaee5",
        mark: <ArrowsSplitIcon weight="duotone" />,
      },
      {
        id: "fabriq",
        title: "Fabriq",
        color: "#8f9cf3",
        mark: <StackIcon weight="duotone" />,
      },
    ],
  },
]

const GLINT_STEP = 1.1 // seconds between one tile's glint and the next

export function AuthLogos() {
  const total = groups.reduce((n, g) => n + g.tiles.length, 0)
  let i = 0

  return (
    <div className="auth-logos">
      {groups.map((group) => (
        <div key={group.area} className={`auth-group ${group.area}`}>
          <span className="auth-group-label">{group.label}</span>
          <ul>
            {group.tiles.map((tile) => {
              const n = i++
              return (
                <li
                  key={tile.id}
                  className="auth-logo"
                  style={
                    {
                      "--i": n,
                      "--c": tile.color,
                      "--cycle": `${total * GLINT_STEP}s`,
                      "--delay": `${n * GLINT_STEP}s`,
                    } as CSSProperties
                  }
                >
                  <span className="auth-logo-glass" title={tile.title}>
                    {tile.mark}
                  </span>
                </li>
              )
            })}
          </ul>
        </div>
      ))}
      <span className="auth-core" aria-hidden="true">
        <svg viewBox="0 0 32 32">
          <path
            fill="hsl(0 0% 96%)"
            d="M0 10.5L0 21A11 11 0 0 0 11 32L28 32A4 4 0 0 0 28 24L11 24A3 3 0 0 1 8 21L8 10.5Z"
          />
          <path
            fill="#5AAEE5"
            d="M32 21.5L32 11A11 11 0 0 0 21 0L4 0A4 4 0 0 0 4 8L21 8A3 3 0 0 1 24 11L24 21.5Z"
          />
        </svg>
      </span>
    </div>
  )
}
