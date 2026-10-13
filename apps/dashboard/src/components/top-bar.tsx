// The bar above every page: where you are, search, the theme, and whatever
// the page's app puts in its slots. The real dashboard and the mock both
// render it, each with its own links and actions.
import { MonitorIcon, MoonIcon, SearchIcon, SunIcon } from "lucide-react"
import { Fragment, type ReactElement, type ReactNode } from "react"

import { useTheme } from "@/components/theme-provider"
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Kbd, KbdGroup } from "@/components/ui/kbd"
import { Separator } from "@/components/ui/separator"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { cn } from "@/lib/utils"

/** A crumb links when it has a link element to render, such as a router Link. */
export type TopBarCrumb = { label: ReactNode; link?: ReactElement }

export function TopBar({
  crumbs,
  onSearch,
  before,
  after,
}: {
  crumbs: TopBarCrumb[]
  onSearch: () => void
  /** Shown before the search button, such as what's in flight. */
  before?: ReactNode
  /** Shown after the theme menu, such as the page's main action. */
  after?: ReactNode
}) {
  return (
    <header className="sticky top-0 z-20 flex h-12 shrink-0 items-center gap-2 border-b bg-background/80 px-3 backdrop-blur-xl supports-[backdrop-filter]:bg-background/70 md:px-4">
      <SidebarTrigger className="-ml-1" />
      <Separator
        orientation="vertical"
        className="mr-1 data-[orientation=vertical]:h-4"
      />
      <Breadcrumb className="min-w-0">
        <BreadcrumbList className="flex-nowrap">
          {crumbs.map((c, i) => (
            <Fragment key={i}>
              {i > 0 && <BreadcrumbSeparator className="hidden md:block" />}
              {/* Every crumb but the last can shrink to an ellipsis, and on
                  narrow screens only the last shows, so the bar stays one
                  line. */}
              <BreadcrumbItem
                className={cn(
                  "min-w-0",
                  i < crumbs.length - 1 && "hidden max-w-48 md:inline-flex"
                )}
              >
                {c.link ? (
                  <BreadcrumbLink
                    render={c.link}
                    className="truncate whitespace-nowrap"
                  >
                    {c.label}
                  </BreadcrumbLink>
                ) : (
                  <BreadcrumbPage className="truncate whitespace-nowrap">
                    {c.label}
                  </BreadcrumbPage>
                )}
              </BreadcrumbItem>
            </Fragment>
          ))}
        </BreadcrumbList>
      </Breadcrumb>
      <div className="ml-auto flex items-center gap-1.5">
        {before}
        <Button
          variant="outline"
          size="sm"
          onClick={onSearch}
          className="hidden w-60 justify-start rounded-full bg-muted/40 pl-3 text-muted-foreground shadow-none md:inline-flex"
        >
          <SearchIcon />
          Search or run a command
          <KbdGroup className="ml-auto">
            <Kbd>⌘</Kbd>
            <Kbd>K</Kbd>
          </KbdGroup>
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onSearch}
          aria-label="Search"
          className="md:hidden"
        >
          <SearchIcon />
        </Button>
        <ThemeMenu />
        {after}
      </div>
    </header>
  )
}

function ThemeMenu() {
  const { theme, setTheme } = useTheme()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="icon-sm" aria-label="Theme" />}
      >
        <SunIcon className="scale-100 rotate-0 transition-all dark:scale-0 dark:-rotate-90" />
        <MoonIcon className="absolute scale-0 rotate-90 transition-all dark:scale-100 dark:rotate-0" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuRadioGroup
          value={theme}
          onValueChange={(v) => setTheme(v as typeof theme)}
        >
          <DropdownMenuRadioItem value="light">
            <SunIcon /> Light
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="dark">
            <MoonIcon /> Dark
          </DropdownMenuRadioItem>
          <DropdownMenuRadioItem value="system">
            <MonitorIcon /> System
          </DropdownMenuRadioItem>
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
