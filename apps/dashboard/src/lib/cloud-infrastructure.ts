export const validExternalTTL = (seconds: number) =>
  Number.isInteger(seconds) && seconds >= 60 && seconds <= 86400

export const canUpdateExternalTTL = (status: string, mode?: string) =>
  status === "ready" && mode === "external"
