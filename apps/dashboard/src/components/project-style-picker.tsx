// The icon grid and color swatches for a project. Shared by the new project
// dialog and the popover on a project's own page.
import { PaletteIcon, ShuffleIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  colorNames,
  iconNames,
  isProjectColor,
  isProjectIcon,
  ProjectIcon,
  type ProjectColorName,
  projectColors,
  type ProjectIconName,
  projectIcons,
  randomColor,
  randomIcon,
} from "@/lib/project-style"
import { cn } from "@/lib/utils"

export function ProjectIconPicker({
  value,
  onChange,
  className,
}: {
  value: ProjectIconName | null
  onChange: (icon: ProjectIconName | null) => void
  className?: string
}) {
  return (
    <ToggleGroup
      spacing={1}
      value={value ? [value] : []}
      onValueChange={(v) => onChange((v[0] as ProjectIconName) ?? null)}
      className={cn("grid w-full", className)}
      aria-label="Icon"
    >
      {iconNames.map((n) => {
        const Icon = projectIcons[n].icon
        return (
          <Tooltip key={n}>
            <TooltipTrigger
              render={
                <ToggleGroupItem
                  value={n}
                  aria-label={projectIcons[n].label}
                  className="size-9 rounded-lg text-muted-foreground aria-pressed:bg-foreground aria-pressed:text-background"
                />
              }
            >
              <Icon />
            </TooltipTrigger>
            <TooltipContent>{projectIcons[n].label}</TooltipContent>
          </Tooltip>
        )
      })}
    </ToggleGroup>
  )
}

export function ProjectColorPicker({
  value,
  onChange,
  compact = false,
  className,
}: {
  value: ProjectColorName | null
  onChange: (color: ProjectColorName | null) => void
  /** Fits all ten swatches on one row of a popover. */
  compact?: boolean
  className?: string
}) {
  return (
    <ToggleGroup
      spacing={compact ? 1 : 2}
      value={value ? [value] : []}
      onValueChange={(v) => onChange((v[0] as ProjectColorName) ?? null)}
      className={cn("flex-wrap", className)}
      aria-label="Color"
    >
      {colorNames.map((n) => (
        <Tooltip key={n}>
          <TooltipTrigger
            render={
              <ToggleGroupItem
                value={n}
                aria-label={projectColors[n].label}
                className={cn(
                  "rounded-full p-0 ring-offset-background aria-pressed:ring-2 aria-pressed:ring-foreground",
                  compact
                    ? "size-7 min-w-7 ring-offset-1"
                    : "size-8 min-w-8 ring-offset-2"
                )}
              />
            }
          >
            <span
              className={cn("rounded-full", compact ? "size-5" : "size-6")}
              style={{ backgroundColor: projectColors[n].value }}
            />
          </TooltipTrigger>
          <TooltipContent>{projectColors[n].label}</TooltipContent>
        </Tooltip>
      ))}
    </ToggleGroup>
  )
}

export type ProjectStyle = { icon: ProjectIconName; color: ProjectColorName }

/**
 * ProjectStylePopover is the project's tile on its own page. Clicking it
 * opens the icon and color pickers; onChange gets each change as it's made,
 * so the caller saves it however it saves things. Without onChange, such as
 * for someone who can't edit the project, the tile is just a tile.
 */
export function ProjectStylePopover({
  name,
  icon,
  color,
  onChange,
}: {
  name: string
  icon: string
  color: string
  onChange?: (change: Partial<ProjectStyle>) => void
}) {
  if (!onChange) return <ProjectIcon icon={icon} color={color} size="lg" />
  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            aria-label={`Change the icon and color of ${name}`}
            className="group/style relative size-10 rounded-xl p-0 hover:bg-transparent aria-expanded:bg-transparent"
          />
        }
      >
        <ProjectIcon icon={icon} color={color} size="lg" />
        <span className="absolute -right-1 -bottom-1 flex size-4.5 items-center justify-center rounded-full border bg-background text-muted-foreground opacity-0 shadow-xs transition-opacity group-hover/style:opacity-100 group-focus-visible/style:opacity-100 group-aria-expanded/style:opacity-100">
          <PaletteIcon className="size-2.5!" />
        </span>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[22rem] gap-4 p-4">
        <PopoverHeader className="flex-row items-start justify-between gap-3">
          <div className="grid gap-1">
            <PopoverTitle>Icon and color</PopoverTitle>
            <PopoverDescription>
              Shown wherever {name} appears.
            </PopoverDescription>
          </div>
          <Button
            variant="outline"
            size="sm"

            onClick={() =>
              onChange({ icon: randomIcon(), color: randomColor() })
            }
          >
            <ShuffleIcon />
            Shuffle
          </Button>
        </PopoverHeader>
        {/* A project always has both, so clicking the picked one again keeps it. */}
        <ProjectIconPicker
          value={isProjectIcon(icon) ? icon : null}
          onChange={(next) => next && onChange({ icon: next })}
          className="grid-cols-8"
        />
        <ProjectColorPicker
          compact
          value={isProjectColor(color) ? color : null}
          onChange={(next) => next && onChange({ color: next })}
        />
      </PopoverContent>
    </Popover>
  )
}
