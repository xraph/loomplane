import { query } from '@forge-go/client-core';
import { op_plans_list } from '../ops/plans.list';
import type { PlanList } from '../types';

export const usePlansList = /*#__PURE__*/ query<PlanList>(op_plans_list);
