// The project's own page as its route renders it: the frame, with one of
// the project's views inside.
import { ProjectFrame } from "@/components/project-frame"
import type { ProjectTab, SettingsSection } from "@/lib/project-nav"
import { ProjectViews } from "@/pages/project-detail"

export function ProjectPage({
  id,
  tab,
  section,
  environment,
}: {
  id: string
  tab?: ProjectTab
  section?: SettingsSection
  environment?: string
}) {
  return (
    <ProjectFrame
      id={id}
      environment={environment}
      place={{
        tab: tab ?? "overview",
        index: true,
        environmentScoped: tab === "variables",
      }}
    >
      <ProjectViews tab={tab} section={section} />
    </ProjectFrame>
  )
}
