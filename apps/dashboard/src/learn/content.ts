// The walkthrough's words: the tour's stops, the guided lessons, and the
// concepts the Learn page explains. Each stop and step points at an anchor,
// a data-tour attribute on a real element, so the copy lives here and the
// pages only say where things are.

/** Anchors are the data-tour names pages put on what a stop points at. */
export const anchors = {
  nav: "nav-main",
  projects: "nav-projects",
  clouds: "nav-clouds",
  waiting: "overview-waiting",
  newDeployment: "new-deployment",
  search: "search",
  help: "help",
  deployProject: "deploy-project",
  deployEnvironment: "deploy-environment",
  deployServices: "deploy-services",
  deployReview: "deploy-review",
  approve: "plan-approve",
  newProject: "new-project",
  newProjectDialog: "new-project-dialog",
  invite: "invite",
  inviteSend: "invite-send",
} as const

export type Anchor = (typeof anchors)[keyof typeof anchors]

/** anchor is the attribute that marks an element as a stop. */
export function anchor(name: Anchor): { "data-tour": Anchor } {
  return { "data-tour": name }
}

export function anchorSelector(name: Anchor): string {
  return `[data-tour="${name}"]`
}

export type TourStop = {
  anchor: Anchor
  title: string
  body: string
  keys?: string[]
}

export const tour: TourStop[] = [
  {
    anchor: anchors.nav,
    title: "Everything starts in the sidebar",
    body: "Overview is your home. Deployments, Projects, Services and Clouds hold the rest, and settings sit at the foot of the sidebar.",
  },
  {
    anchor: anchors.projects,
    title: "Projects are the things you ship",
    body: "A project is one app or system: its services, its environments, and where each one runs. Your projects stay pinned here.",
  },
  {
    anchor: anchors.waiting,
    title: "Nothing ships without a yes",
    body: "Loomplane plans every change first and shows you what it will create, update or delete. A plan waits here until someone approves it.",
  },
  {
    anchor: anchors.newDeployment,
    title: "Start a deploy from anywhere",
    body: "Pick a project and an environment and Loomplane writes the plan. You review it before anything changes.",
    keys: ["N"],
  },
  {
    anchor: anchors.search,
    title: "Find anything, run anything",
    body: "Search projects, services and deployments, or type what you want to do, like connect a cloud or invite someone.",
    keys: ["⌘", "K"],
  },
  {
    anchor: anchors.clouds,
    title: "Your code runs in your clouds",
    body: "Connect the cloud accounts you already use. Loomplane deploys into accounts you own and asks only for the access it needs.",
  },
]

/** How a lesson step knows you did it. */
export type Advance =
  | { kind: "click" }
  | { kind: "path"; match: RegExp }
  | { kind: "next"; label: string }

export type LessonStep = {
  anchor: Anchor
  title: string
  body: string
  /** What the panel says while it waits for you. */
  waiting?: string
  advance: Advance
  /** Where to go when the step's anchor isn't on the page you're on. */
  route?: LessonRoute
}

/** The few pages a lesson can send you to. */
export type LessonRoute = "/" | "/deployments/new" | "/projects" | "/members"

export type Topic = "start" | "deploy" | "team"

export const topics: { id: Topic; label: string }[] = [
  { id: "start", label: "Getting started" },
  { id: "deploy", label: "Deploying" },
  { id: "team", label: "Team" },
]

export type Lesson = {
  id: string
  topic: Topic
  title: string
  summary: string
  minutes: number
  steps: LessonStep[]
}

export const lessons: Lesson[] = [
  {
    id: "first-deploy",
    topic: "deploy",
    title: "Ship your first deploy",
    summary:
      "Pick a project and an environment, read the plan, then approve it and watch it go out.",
    minutes: 5,
    steps: [
      {
        anchor: anchors.deployProject,
        title: "Pick a project",
        body: "A project is one app or system. The first one is picked for you; choose another here if you like.",
        advance: { kind: "next", label: "This one" },
        route: "/deployments/new",
      },
      {
        anchor: anchors.deployEnvironment,
        title: "Pick an environment",
        body: "Start somewhere your customers won't notice, like Preview or Staging, if the project has one.",
        waiting: "Waiting for you to pick an environment",
        advance: { kind: "click" },
        route: "/deployments/new",
      },
      {
        anchor: anchors.deployServices,
        title: "Check what goes out",
        body: "Services that changed since the last deploy are ticked. Untick one to leave it out, or use Change to move where it runs.",
        advance: { kind: "next", label: "Looks right" },
        route: "/deployments/new",
      },
      {
        anchor: anchors.deployReview,
        title: "Plan the deploy",
        body: "Loomplane works out every change it would make before it touches a cloud.",
        waiting: "Waiting for you to click Review plan",
        advance: { kind: "path", match: /^\/deployments\/(?!new$)[^/]+$/ },
        route: "/deployments/new",
      },
      {
        anchor: anchors.approve,
        title: "Read the plan and approve",
        body: "Each change is listed with what it touches. Approve when you're happy, or reject it with a note. You can still back out here.",
        waiting: "Waiting for a decision on this plan",
        advance: { kind: "click" },
      },
    ],
  },
  {
    id: "first-project",
    topic: "start",
    title: "Create a project from a repository",
    summary:
      "Point Loomplane at a repository and let it find the services inside.",
    minutes: 3,
    steps: [
      {
        anchor: anchors.newProject,
        title: "Start a new project",
        body: "Projects live in the sidebar. New project opens a short form.",
        waiting: "Waiting for you to click New project",
        advance: { kind: "click" },
        route: "/projects",
      },
      {
        anchor: anchors.newProjectDialog,
        title: "Name it and add a repository",
        body: "Give the project a name and pick the repository it ships. You can add more repositories later.",
        waiting: "Waiting for you to create the project",
        advance: { kind: "path", match: /^\/projects\/[^/]+$/ },
      },
    ],
  },
  {
    id: "invite-teammate",
    topic: "team",
    title: "Invite a teammate",
    summary:
      "Add someone to the workspace and choose what they can do, including approving deploys.",
    minutes: 2,
    steps: [
      {
        anchor: anchors.invite,
        title: "Open the invite form",
        body: "Members lists everyone in the workspace and what each person can do.",
        waiting: "Waiting for you to click Invite",
        advance: { kind: "click" },
        route: "/members",
      },
      {
        anchor: anchors.inviteSend,
        title: "Create the invitation",
        body: "Enter their email and pick a role. Loomplane emails them a link when mail is set up, and shows you the link to share either way.",
        waiting: "Waiting for you to create it",
        advance: { kind: "click" },
      },
    ],
  },
]

export function lessonById(id: string): Lesson | undefined {
  return lessons.find((l) => l.id === id)
}

export type Concept = { word: string; body: string }

export const concepts: Concept[] = [
  {
    word: "Workspace",
    body: "Your team's home in Loomplane. It holds projects, cloud accounts, members and settings.",
  },
  {
    word: "Project",
    body: "One app or system, read from one or more repositories. It's the thing you deploy.",
  },
  {
    word: "Service",
    body: "A part of a project that runs on its own: a web app, an API, a worker or a database.",
  },
  {
    word: "Environment",
    body: "A full copy of a project, like Preview, Staging or Production, each with its own variables.",
  },
  {
    word: "Cloud account",
    body: "An account at a provider you connect. Services run there, and you keep owning it.",
  },
  {
    word: "Plan",
    body: "Everything a deploy would change, worked out before anything happens.",
  },
  {
    word: "Approval",
    body: "The yes a plan needs before it ships. Plans nobody approves close on their own.",
  },
  {
    word: "Deployment",
    body: "An approved plan being carried out, with its logs, its result and a way back.",
  },
]

/** waitingTerm explains the Overview's "Waiting on you" card. */
export const waitingTerm: Concept = {
  word: "Waiting on you",
  body: "A plan holding until someone approves it. Review it to see each change, then approve to deploy. If nobody does by the date shown, it closes and nothing changes.",
}
