import { createFileRoute } from "@tanstack/react-router"

// App renders this screen itself: the auth form while you're signed out, and
// a redirect home once you're signed in. The route only reserves the URL, so
// it renders nothing; a redirect here would fire for the one render where a
// client-side move to a recovery page still matches this route.
export const Route = createFileRoute("/sign-up")({
  component: () => null,
})
