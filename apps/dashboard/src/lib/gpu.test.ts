import { expect, it } from "vitest"
import { formatHourly, formatMonthly, gpuTitle } from "@/lib/gpu"

it("formats an hourly price, or says it is unknown", () => {
  expect(formatHourly(3.39, true)).toBe("$3.39/hr")
  expect(formatHourly(0, false)).toBe("price unknown")
})
it("estimates a month at 730 hours", () => {
  expect(formatMonthly(3.39)).toBe("about $2,475/mo")
})
it("titles known kinds and falls back to the kind ID", () => {
  expect(gpuTitle("nvidia-h100-80gb")).toBe("NVIDIA H100 80GB")
  expect(gpuTitle("nvidia-geforce-rtx-4090")).toBe("nvidia-geforce-rtx-4090")
})
