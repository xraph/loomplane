// A project's icon and color: picked when it's created, random when the
// person doesn't pick. The icons are lucide's, chosen for what software
// projects are about; the colors sit at about the same lightness so no
// project shouts louder than another.
import {
  AnchorIcon,
  AtomIcon,
  BellIcon,
  BookOpenIcon,
  BotIcon,
  BracesIcon,
  CalendarDaysIcon,
  ChartLineIcon,
  CompassIcon,
  CpuIcon,
  CreditCardIcon,
  DatabaseIcon,
  FilmIcon,
  FingerprintIcon,
  FlameIcon,
  Gamepad2Icon,
  GlobeIcon,
  GraduationCapIcon,
  HeartPulseIcon,
  HexagonIcon,
  ImageIcon,
  KeyRoundIcon,
  LayersIcon,
  LeafIcon,
  type LucideIcon,
  MailIcon,
  MapPinIcon,
  MegaphoneIcon,
  MessageCircleIcon,
  MountainIcon,
  MusicIcon,
  PackageIcon,
  RadarIcon,
  ReceiptIcon,
  RocketIcon,
  SearchIcon,
  ServerIcon,
  ShieldIcon,
  ShoppingBagIcon,
  SmartphoneIcon,
  SparklesIcon,
  StoreIcon,
  TerminalIcon,
  TruckIcon,
  UsersIcon,
  WalletIcon,
  WebhookIcon,
  WorkflowIcon,
  ZapIcon,
} from "lucide-react"
import type { CSSProperties } from "react"

import { cn } from "@/lib/utils"

export type ProjectIconName = keyof typeof projectIcons

/**
 * Forty-eight icons in four rows of twelve, grouped the way people think
 * about what they're building. Forty-eight also fills an eight-wide grid, so
 * neither picker ends on a lone icon.
 */
export const projectIcons = {
  // Commerce, then people and messages
  store: { label: "Storefront", icon: StoreIcon },
  bag: { label: "Shop", icon: ShoppingBagIcon },
  card: { label: "Payments", icon: CreditCardIcon },
  receipt: { label: "Billing", icon: ReceiptIcon },
  wallet: { label: "Wallet", icon: WalletIcon },
  truck: { label: "Logistics", icon: TruckIcon },
  bell: { label: "Notifications", icon: BellIcon },
  mail: { label: "Email", icon: MailIcon },
  chat: { label: "Chat", icon: MessageCircleIcon },
  megaphone: { label: "Marketing", icon: MegaphoneIcon },
  users: { label: "Community", icon: UsersIcon },
  calendar: { label: "Scheduling", icon: CalendarDaysIcon },
  // Data and AI, then platform and developer tools
  database: { label: "Data", icon: DatabaseIcon },
  chart: { label: "Analytics", icon: ChartLineIcon },
  search: { label: "Search", icon: SearchIcon },
  sparkles: { label: "AI", icon: SparklesIcon },
  bot: { label: "Agents", icon: BotIcon },
  radar: { label: "Monitoring", icon: RadarIcon },
  server: { label: "Backend", icon: ServerIcon },
  cpu: { label: "Compute", icon: CpuIcon },
  webhook: { label: "Webhooks", icon: WebhookIcon },
  braces: { label: "API", icon: BracesIcon },
  terminal: { label: "Tooling", icon: TerminalIcon },
  workflow: { label: "Pipelines", icon: WorkflowIcon },
  // Kinds of product and content
  package: { label: "Library", icon: PackageIcon },
  layers: { label: "Platform", icon: LayersIcon },
  smartphone: { label: "Mobile", icon: SmartphoneIcon },
  globe: { label: "Web", icon: GlobeIcon },
  pin: { label: "Maps", icon: MapPinIcon },
  health: { label: "Health", icon: HeartPulseIcon },
  learning: { label: "Learning", icon: GraduationCapIcon },
  book: { label: "Docs", icon: BookOpenIcon },
  film: { label: "Video", icon: FilmIcon },
  music: { label: "Audio", icon: MusicIcon },
  image: { label: "Images", icon: ImageIcon },
  gamepad: { label: "Games", icon: Gamepad2Icon },
  // Trust, then marks with no subject, for codenames
  shield: { label: "Security", icon: ShieldIcon },
  key: { label: "Auth", icon: KeyRoundIcon },
  fingerprint: { label: "Identity", icon: FingerprintIcon },
  rocket: { label: "Launch", icon: RocketIcon },
  zap: { label: "Realtime", icon: ZapIcon },
  compass: { label: "Compass", icon: CompassIcon },
  anchor: { label: "Anchor", icon: AnchorIcon },
  leaf: { label: "Leaf", icon: LeafIcon },
  flame: { label: "Flame", icon: FlameIcon },
  mountain: { label: "Mountain", icon: MountainIcon },
  atom: { label: "Atom", icon: AtomIcon },
  hexagon: { label: "Hexagon", icon: HexagonIcon },
} satisfies Record<string, { label: string; icon: LucideIcon }>

export type ProjectColorName = keyof typeof projectColors

/** Eight hues at about the same lightness, plus the brand's own two. */
export const projectColors = {
  gold: { label: "Weld gold", value: "var(--weld)" },
  orange: { label: "Orange", value: "oklch(0.7 0.16 50)" },
  red: { label: "Red", value: "oklch(0.64 0.18 25)" },
  rose: { label: "Rose", value: "oklch(0.66 0.17 0)" },
  violet: { label: "Violet", value: "oklch(0.62 0.17 300)" },
  indigo: { label: "Indigo", value: "oklch(0.6 0.16 270)" },
  blue: { label: "Blue", value: "oklch(0.64 0.14 245)" },
  teal: { label: "Teal", value: "oklch(0.66 0.11 190)" },
  green: { label: "Overdye green", value: "var(--overdye)" },
  slate: { label: "Slate", value: "oklch(0.58 0.03 260)" },
} satisfies Record<string, { label: string; value: string }>

export const iconNames = Object.keys(projectIcons) as ProjectIconName[]
export const colorNames = Object.keys(projectColors) as ProjectColorName[]

/** isProjectIcon reports whether name is one of the icons, such as one the API returned. */
export function isProjectIcon(name: string): name is ProjectIconName {
  return Object.hasOwn(projectIcons, name)
}

/** isProjectColor reports whether name is one of the colors. */
export function isProjectColor(name: string): name is ProjectColorName {
  return Object.hasOwn(projectColors, name)
}

/**
 * projectStyle is the icon and color to draw a project with. The server
 * gives every project both, but a name it sends that this build doesn't
 * know, or a missing one, gets a pick derived from the project's ID, so a
 * project looks the same on every page and every reload.
 */
export function projectStyle(p: {
  id: string
  icon?: string
  color?: string
}): { icon: ProjectIconName; color: ProjectColorName } {
  const seed = hash(p.id)
  return {
    icon:
      p.icon && isProjectIcon(p.icon)
        ? p.icon
        : (iconNames[seed % iconNames.length] as ProjectIconName),
    color:
      p.color && isProjectColor(p.color)
        ? p.color
        : (colorNames[seed % colorNames.length] as ProjectColorName),
  }
}

// FNV-1a: small, stable, and spreads short IDs well enough for a picker.
function hash(s: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

export function randomIcon(): ProjectIconName {
  return iconNames[
    Math.floor(Math.random() * iconNames.length)
  ] as ProjectIconName
}

export function randomColor(): ProjectColorName {
  return colorNames[
    Math.floor(Math.random() * colorNames.length)
  ] as ProjectColorName
}

// The glyph sizes are important because sidebar buttons, command items and
// select items all size every svg inside them to 16px.
const sizes = {
  xs: "size-5 rounded-[5px] [&_svg]:size-3!",
  sm: "size-6 rounded-md [&_svg]:size-3.5!",
  md: "size-8 rounded-lg [&_svg]:size-4!",
  lg: "size-10 rounded-xl [&_svg]:size-5!",
  xl: "size-12 rounded-xl [&_svg]:size-6!",
} as const

/**
 * ProjectIcon is the project's icon on a tint of its color. It takes plain
 * strings because the API sends them, and falls back to a slate hexagon for
 * a name it doesn't know, such as one a newer server added.
 */
export function ProjectIcon({
  icon,
  color,
  size = "sm",
  className,
}: {
  icon: string
  color: string
  size?: keyof typeof sizes
  className?: string
}) {
  const Icon = isProjectIcon(icon) ? projectIcons[icon].icon : HexagonIcon
  const value = isProjectColor(color)
    ? projectColors[color].value
    : projectColors.slate.value
  return (
    <span
      aria-hidden="true"
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center",
        sizes[size],
        className
      )}
      style={
        {
          "--project": value,
          backgroundColor:
            "color-mix(in oklch, var(--project) 16%, transparent)",
          color: "color-mix(in oklch, var(--project) 82%, var(--foreground))",
        } as CSSProperties
      }
    >
      <Icon />
    </span>
  )
}
