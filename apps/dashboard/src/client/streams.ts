/**
 * The stream endpoints the dashboard opens itself. Forge keeps WebSocket and
 * SSE routes out of the REST operation manifest, so their paths live here,
 * and streams.test.ts checks each one against the generated specs.
 *
 * Hand-written, and outside forge/ on purpose: `forge client check` reports
 * any file under forge/ it didn't write.
 */

/** The organization's live channel over WebSocket. */
export const REALTIME_WS = "/api/v1/realtime/ws"

/** The same channel over Server-Sent Events, the WebSocket's fallback. */
export const REALTIME_EVENTS = "/api/v1/realtime/events"

/** agentTaskStream is the SSE path that follows one agent task's events. */
export function agentTaskStream(taskId: string): string {
  return `/api/v1/agent/tasks/${encodeURIComponent(taskId)}/stream`
}
