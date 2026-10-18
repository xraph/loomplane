import { useEffect } from "react"

import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { AppSidebar } from "@/mock/components/sidebar-07/app-sidebar"
import { StatusBar } from "@/mock/components/status-bar"
import { DeploymentPage } from "@/mock/pages/deployment"
import { CloudsPage } from "@/mock/pages/clouds"
import { DeploymentsPage } from "@/mock/pages/deployments"
import { InfrastructurePage } from "@/mock/pages/infrastructure"
import { OverviewPage } from "@/mock/pages/overview"
import { ProjectPage } from "@/mock/pages/project"
import { match, useRoute } from "@/mock/router"

export function MockApp() {
  return (
    <SidebarProvider>
      <AppSidebar />
      <SidebarInset className="min-w-0">
        <Routes />
        <StatusBar />
      </SidebarInset>
    </SidebarProvider>
  )
}

function Routes() {
  const route = useRoute()
  useEffect(() => {
    window.scrollTo({ top: 0 })
  }, [route])

  const deployment = match("/deployments/:id", route)
  if (deployment?.id) {
    // Keyed so tabs and the step filter start fresh for each deployment.
    return <DeploymentPage key={deployment.id} id={deployment.id} />
  }
  const project = match("/projects/:id", route)
  if (project?.id) {
    return <ProjectPage key={project.id} id={project.id} />
  }
  if (route === "/deployments") return <DeploymentsPage />
  if (route === "/clouds") return <CloudsPage />
  if (route === "/infrastructure") return <InfrastructurePage />
  return <OverviewPage />
}
