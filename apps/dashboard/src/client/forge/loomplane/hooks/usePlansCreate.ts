import { mutation } from '@forge-go/client-core';
import { op_plans_create } from '../ops/plans.create';
import type { Plan } from '../types';

export const usePlansCreate = /*#__PURE__*/ mutation<Plan, Plan>(op_plans_create);
