// The walkthrough, mounted once per workspace: the welcome on a first
// visit, the tour, and a guided lesson's panel, plus what every page uses
// to start them.
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react"

import { useOrganization } from "@/lib/organization"
import { usePath } from "@/lib/router"

import { useChecklist } from "./checklist"
import { lessonById } from "./content"
import { LessonPanel } from "./lesson-panel"
import {
  type Progress,
  emptyProgress,
  moveLesson,
  progressKey,
  useProgress,
} from "./progress"
import { Tour } from "./tour"
import { WelcomeDialog } from "./welcome"

type Learn = {
  progress: Progress
  update: (change: (p: Progress) => Progress) => void
  startTour: () => void
  /** startLesson opens a lesson's panel, from the top or where you left it. */
  startLesson: (id: string, fromStart?: boolean) => void
  stopLesson: () => void
}

const LearnContext = createContext<Learn | null>(null)

const quiet: Learn = {
  progress: emptyProgress,
  update: () => {},
  startTour: () => {},
  startLesson: () => {},
  stopLesson: () => {},
}

/** useLearn starts the tour and lessons. Outside a workspace it does
 * nothing, so shared parts like the top bar render anywhere. */
export function useLearn(): Learn {
  return useContext(LearnContext) ?? quiet
}

export function LearnProvider({ children }: { children: ReactNode }) {
  const { session } = useOrganization()
  const key = progressKey(session.userId ?? "anyone", session.orgId)
  const [progress, update] = useProgress(key)
  const [touring, setTouring] = useState(false)
  const path = usePath()

  const startTour = useCallback(() => {
    update((p) => ({ ...p, active: undefined, welcomed: true }))
    setTouring(true)
  }, [update])
  const startLesson = useCallback(
    (id: string, fromStart = false) => {
      setTouring(false)
      update((p) => {
        const at = p.lessons[id]
        const next =
          fromStart || !at || at.done ? moveLesson(p, id, 0) : { ...p }
        return { ...next, active: id, welcomed: true }
      })
    },
    [update]
  )
  const stopLesson = useCallback(
    () => update((p) => ({ ...p, active: undefined })),
    [update]
  )

  const value = useMemo(
    () => ({ progress, update, startTour, startLesson, stopLesson }),
    [progress, update, startTour, startLesson, stopLesson]
  )
  const lesson = progress.active ? lessonById(progress.active) : undefined

  return (
    <LearnContext.Provider value={value}>
      {children}
      {!progress.welcomed && !touring && path === "/" && (
        <FirstVisit progress={progress} update={update} />
      )}
      {touring && (
        <Tour
          onEnd={(finished) => {
            setTouring(false)
            update((p) => ({
              ...p,
              welcomed: true,
              toured: p.toured || finished,
            }))
          }}
        />
      )}
      {lesson && !touring && (
        <LessonPanel
          lesson={lesson}
          at={progress.lessons[lesson.id] ?? { step: 0, done: false }}
          onMove={(step) =>
            update((p) =>
              moveLesson(p, lesson.id, step, step >= lesson.steps.length)
            )
          }
          onExit={stopLesson}
        />
      )}
    </LearnContext.Provider>
  )
}

// The welcome shows once, on the Overview, to someone whose workspace
// still has setting up to do. A workspace that's already running doesn't
// need it, so that visit counts as having seen it.
function FirstVisit({
  progress,
  update,
}: {
  progress: Progress
  update: Learn["update"]
}) {
  const { startTour, startLesson } = useLearn()
  const { tasks, loaded } = useChecklist(progress.ticked)
  const fresh = tasks.some((t) => !t.done && !t.manual)
  useEffect(() => {
    if (loaded && !fresh) update((p) => ({ ...p, welcomed: true }))
  }, [loaded, fresh, update])
  if (!loaded || !fresh) return null
  // The guided option teaches the next thing this workspace needs.
  const next =
    lessonById(tasks.find((t) => !t.done && t.lesson)?.lesson ?? "") ??
    lessonById("first-deploy")
  if (!next) return null
  return (
    <WelcomeDialog
      lesson={next}
      onTour={startTour}
      onLesson={() => startLesson(next.id, true)}
      onClose={() => update((p) => ({ ...p, welcomed: true }))}
    />
  )
}
