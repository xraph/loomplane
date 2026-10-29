import { query } from '@forge-go/client-core';
import { op_plans_soak } from '../ops/plans.soak';
import type { Soak } from '../types';

export const usePlansSoak = /*#__PURE__*/ query<Soak>(op_plans_soak);
