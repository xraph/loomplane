// The bar above every page: where you are, search, the theme, and whatever
// the page's app puts in its slots. The real dashboard and the mock both
// render it, each with its own links and actions.
import { MonitorIcon, MoonIcon, SearchIcon, SunIcon } from "lucide-react"
import { Fragment, type ReactElement, type ReactNode } from "react"

import { AccentPicker } from "@/components/accent-picker"
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
import { Kbd } from "@/components/ui/kbd"
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
    <header className="sticky top-0 z-20 flex min-h-[57px] shrink-0 flex-wrap items-center gap-x-3 gap-y-2.5 border-b border-border-tab bg-background px-4 py-2.5">
      <SidebarTrigger className="size-8 rounded-lg text-caption" />
      <Breadcrumb className="min-w-0">
        <BreadcrumbList className="flex-nowrap gap-1.5 text-[13.5px] leading-[normal] sm:gap-1.5">
          {crumbs.map((c, i) => (
            <Fragment key={i}>
              {i > 0 && (
                <BreadcrumbSeparator className="hidden text-[#b9b1aa] md:block dark:text-muted-foreground">
                  /
                </BreadcrumbSeparator>
              )}
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
      <div className="ml-auto flex flex-wrap items-center justify-end gap-3">
        {before}
        <Button
          variant="outline"
          size="sm"
          onClick={onSearch}
          className="hidden h-[34px] min-w-60 justify-start gap-2.5 rounded-[10px] bg-subtle pr-2 pl-3 text-[13px] text-caption shadow-none md:inline-flex"
        >
          <SearchIcon />
          Search or run a command
          <Kbd className="ml-auto h-auto rounded-md border border-input bg-background px-1.5 py-0.5 font-mono text-[11px] leading-[normal] text-label">
            ⌘K
          </Kbd>
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
        <AccentPicker />
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
