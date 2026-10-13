import { query } from '@forge-go/client-core';
import { op_realtime_snapshot } from '../ops/realtime.snapshot';
import type { Snapshot } from '../types';

export const useRealtimeSnapshot = /*#__PURE__*/ query<Snapshot>(op_realtime_snapshot);
