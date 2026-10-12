import { mutation } from '@forge-go/client-core';
import { op_plans_approve } from '../ops/plans.approve';
import type { Plan } from '../types';

export const usePlansApprove = /*#__PURE__*/ mutation<Plan, Plan>(op_plans_approve);
