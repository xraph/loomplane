import { act, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { checklistTasks } from "./checklist"
import { type Lesson, lessons, tour } from "./content"
import { lessonsFor, resumable } from "./help-menu"
import { LessonPanel } from "./lesson-panel"
import {
  emptyProgress,
  moveLesson,
  parseProgress,
  progressKey,
  tick,
} from "./progress"
import { place } from "./spotlight"
import { visibleStops } from "./tour"

const { navigate, path } = vi.hoisted(() => ({
  navigate: vi.fn(),
  path: { current: "/" },
}))
vi.mock("@/lib/router", () => ({
  useNavigate: () => navigate,
  usePath: () => path.current,
  Link: ({
    children,
    to,
    ...rest
  }: React.ComponentProps<"a"> & { to: string }) => (
    <a {...rest} href={to}>
      {children}
    </a>
  ),
}))

describe("progress", () => {
  it("keys by person and workspace", () => {
    expect(progressKey("u1", "o1")).not.toBe(progressKey("u1", "o2"))
    expect(progressKey("u1", "o1")).not.toBe(progressKey("u2", "o1"))
  })

  it("reads nothing, junk and old records safely", () => {
    expect(parseProgress(null)).toEqual(emptyProgress)
    expect(parseProgress("{not json")).toEqual(emptyProgress)
    expect(
      parseProgress(
        JSON.stringify({
          toured: true,
          ticked: ["alerts", 3],
          lessons: { a: { step: -2, done: 1 }, b: "nope" },
          active: "a",
        })
      )
    ).toEqual({
      ...emptyProgress,
      toured: true,
      ticked: ["alerts"],
      lessons: { a: { step: 0, done: true } },
      active: "a",
    })
  })

  it("ticks and moves lessons without touching the rest", () => {
    const p = tick(emptyProgress, "alerts", true)
    expect(p.ticked).toEqual(["alerts"])
    expect(tick(p, "alerts", true)).toBe(p)
    expect(tick(p, "alerts", false).ticked).toEqual([])
    expect(moveLesson(p, "x", 2).lessons.x).toEqual({ step: 2, done: false })
  })
})

describe("checklist", () => {
  const byId = (tasks: ReturnType<typeof checklistTasks>) =>
    Object.fromEntries(tasks.map((t) => [t.id, t]))

  it("ticks what the workspace already has", () => {
    const t = byId(
      checklistTasks(
        { projects: 1, clouds: 0, codeHosts: 0, succeeded: 0, members: 1 },
        []
      )
    )
    expect(t.code.done).toBe(true) // a project means code is connected
    expect(t.project.done).toBe(true)
    expect(t.cloud.done).toBe(false)
    expect(t.deploy.done).toBe(false)
    expect(t.team.done).toBe(false) // only you
    expect(t.alerts.manual).toBe(true)
  })

  it("lets unknown facts be ticked by hand, and only those", () => {
    const t = byId(checklistTasks({ projects: 0 }, ["team", "project"]))
    expect(t.team.manual).toBe(true)
    expect(t.team.done).toBe(true)
    expect(t.project.manual).toBe(false)
    expect(t.project.done).toBe(false)
  })

  it("points the tasks with lessons at real lessons", () => {
    for (const task of checklistTasks({}, []))
      if (task.lesson)
        expect(lessons.some((l) => l.id === task.lesson)).toBe(true)
  })
})

describe("place", () => {
  const view = { width: 1440, height: 900 }
  const card = { width: 340, height: 200 }

  it("puts the card beside the sidebar", () => {
    const p = place({ top: 72, left: 6, width: 252, height: 334 }, card, view)
    expect(p.side).toBe("right")
    expect(p.left).toBe(6 + 252 + 14)
  })

  it("puts the card under a wide card and keeps it on screen", () => {
    const p = place(
      { top: 92, left: 1224, width: 194, height: 46 },
      card,
      view
    )
    expect(p.side).toBe("bottom")
    expect(p.left + card.width).toBeLessThanOrEqual(view.width - 8)
    // The arrow still aims at the target's middle.
    expect(p.left + p.arrow).toBeCloseTo(1224 + 97)
  })

  it("centers when there's no room anywhere", () => {
    const p = place(
      { top: 0, left: 0, width: 1440, height: 900 },
      card,
      view
    )
    expect(p.side).toBe("center")
  })
})

describe("tour and help", () => {
  it("leaves out stops that aren't on screen", () => {
    const shown = visibleStops(tour, (s) => s.anchor !== "nav-main")
    expect(shown).toHaveLength(tour.length - 1)
  })

  it("offers the lessons that start on this page", () => {
    expect(lessonsFor("/members").map((l) => l.id)).toEqual(["invite-teammate"])
    expect(lessonsFor("/nowhere")).toEqual([])
  })

  it("resumes the latest unfinished lesson", () => {
    expect(
      resumable({
        "first-project": { step: 1, done: false },
        "first-deploy": { step: 2, done: true },
      })?.lesson.id
    ).toBe("first-project")
    expect(resumable({ "first-deploy": { step: 0, done: false } })).toBe(
      undefined
    )
  })
})

describe("lesson panel", () => {
  const lesson: Lesson = {
    id: "demo",
    topic: "start",
    title: "Demo",
    summary: "",
    minutes: 1,
    steps: [
      {
        anchor: "invite",
        title: "Click invite",
        body: "Body one",
        waiting: "Waiting for a click",
        advance: { kind: "click" },
      },
      {
        anchor: "invite-send",
        title: "Say done",
        body: "Body two",
        advance: { kind: "next", label: "Done" },
      },
    ],
  }

  it("moves on when you click the ringed element", async () => {
    const onMove = vi.fn()
    render(
      <>
        <button type="button" data-tour="invite">
          Invite
        </button>
        <LessonPanel
          lesson={lesson}
          at={{ step: 0, done: false }}
          onMove={onMove}
          onExit={() => {}}
        />
      </>
    )
    expect(screen.getByText("Waiting for a click")).toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: "Invite" }))
    await act(() => new Promise((r) => setTimeout(r, 0)))
    expect(onMove).toHaveBeenCalledWith(1)
  })

  it("moves on from a step you confirm, and says when it's done", async () => {
    const onMove = vi.fn()
    const { rerender } = render(
      <LessonPanel
        lesson={lesson}
        at={{ step: 1, done: false }}
        onMove={onMove}
        onExit={() => {}}
      />
    )
    await userEvent.click(screen.getByRole("button", { name: "Done" }))
    expect(onMove).toHaveBeenCalledWith(2)
    rerender(
      <LessonPanel
        lesson={lesson}
        at={{ step: 2, done: true }}
        onMove={onMove}
        onExit={() => {}}
      />
    )
    expect(screen.getByText(/That's the lesson/)).toBeInTheDocument()
  })

  it("moves on when you reach the page a step leads to", () => {
    const onMove = vi.fn()
    path.current = "/projects/p1"
    render(
      <LessonPanel
        lesson={{
          ...lesson,
          steps: [
            {
              anchor: "new-project-dialog",
              title: "Create it",
              body: "",
              advance: { kind: "path", match: /^\/projects\/[^/]+$/ },
            },
          ],
        }}
        at={{ step: 0, done: false }}
        onMove={onMove}
        onExit={() => {}}
      />
    )
    expect(onMove).toHaveBeenCalledWith(1)
    path.current = "/"
  })
})
