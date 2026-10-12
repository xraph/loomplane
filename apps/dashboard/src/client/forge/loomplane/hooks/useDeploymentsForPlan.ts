import { query } from '@forge-go/client-core';
import { op_deployments_forPlan } from '../ops/deployments.forPlan';
import type { Deployment } from '../types';

export const useDeploymentsForPlan = /*#__PURE__*/ query<Deployment>(op_deployments_forPlan);
