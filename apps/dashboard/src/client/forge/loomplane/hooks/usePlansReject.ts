import { mutation } from '@forge-go/client-core';
import { op_plans_reject } from '../ops/plans.reject';
import type { Plan } from '../types';

export const usePlansReject = /*#__PURE__*/ mutation<Plan, Plan>(op_plans_reject);
