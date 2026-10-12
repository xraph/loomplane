import { query } from '@forge-go/client-core';
import { op_plans_get } from '../ops/plans.get';
import type { Plan } from '../types';

export const usePlansGet = /*#__PURE__*/ query<Plan>(op_plans_get);
