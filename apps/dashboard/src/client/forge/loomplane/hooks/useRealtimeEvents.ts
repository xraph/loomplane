import { query } from '@forge-go/client-core';
import { op_realtime_events } from '../ops/realtime.events';

export const useRealtimeEvents = /*#__PURE__*/ query(op_realtime_events);
