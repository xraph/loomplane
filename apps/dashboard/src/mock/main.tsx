// The Loomplane dashboard mock: a separate entry (mock/index.html, /mock/ in
// dev) with made-up data, for working on the interface without a backend.
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import "../index.css"
import { ThemeProvider } from "@/components/theme-provider"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { MockApp } from "@/mock/app"
import { ShellProvider } from "@/mock/shell"
import { MockStoreProvider } from "@/mock/store"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider>
      <TooltipProvider>
        <MockStoreProvider>
          <ShellProvider>
            <MockApp />
          </ShellProvider>
        </MockStoreProvider>
        <Toaster />
      </TooltipProvider>
    </ThemeProvider>
  </StrictMode>
)
