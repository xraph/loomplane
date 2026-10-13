import { query } from '@forge-go/client-core';
import { op_activity_get } from '../ops/activity.get';
import type { ActivitySummary } from '../types';

export const useActivityGet = /*#__PURE__*/ query<ActivitySummary>(op_activity_get);
