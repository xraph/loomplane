// What the server has switched on, from its public config. The dashboard
// hides a feature's pages and links when it's off, and a route for one
// sends you home.
import { redirect } from "@tanstack/react-router"
import { createContext, type ReactNode, useContext, useMemo } from "react"

export type Features = { ai: boolean }

const off: Features = { ai: false }

const FeaturesContext = createContext<Features>(off)

// Routes check features before anything renders, outside React, so the
// dashboard records them here once at startup, from the same public config
// the provider gets.
const current: { features: Features } = { features: off }

/**
 * setFeatures records what the server has on, for requireFeature. main.tsx
 * calls it once, before the router loads; tests call it to switch AI on.
 */
export function setFeatures(features: Features | undefined): void {
  current.features = { ...off, ...features }
}

export function FeaturesProvider({
  features,
  children,
}: {
  features: Features | undefined
  children: ReactNode
}) {
  const value = useMemo(() => ({ ...off, ...features }), [features])
  return (
    <FeaturesContext.Provider value={value}>
      {children}
    </FeaturesContext.Provider>
  )
}

/** useFeatures says what the server has on. Everything is off by default. */
export function useFeatures(): Features {
  return useContext(FeaturesContext)
}

/**
 * requireFeature is for a route's beforeLoad: it sends you to the overview
 * when the server has the feature off.
 */
export function requireFeature(feature: keyof Features): void {
  if (!current.features[feature]) throw redirect({ to: "/", replace: true })
}
