// What the server says about this instance before anyone signs in: whether
// first-run setup is waiting, and whether sign-up needs an invitation or an
// approved waitlist entry.

export type Instance = {
  /** No account exists yet, so the dashboard asks for setup first. */
  setupPending: boolean
  /** Sign-up needs an invitation or an approved waitlist entry. */
  waitlist: boolean
  /** Where the instance console lives. Platform admins only. */
  consolePath?: string
}

const current: { instance: Instance } = {
  instance: { setupPending: false, waitlist: false },
}

/**
 * setInstance records the server's answer once at startup, before the router
 * loads. A visitor needs it on the first render, so it isn't fetched again in
 * React; tests call it to put the instance in a state.
 */
export function setInstance(instance: Instance): void {
  current.instance = instance
}

/** useInstance says what the server reported at startup. */
export function useInstance(): Instance {
  return current.instance
}
