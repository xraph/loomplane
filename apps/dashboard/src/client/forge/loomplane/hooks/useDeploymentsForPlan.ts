import { query } from '@forge-go/client-core';
import { op_deployments_forPlan } from '../ops/deployments.forPlan';
import type { DeploymentsDeployment } from '../types';

export const useDeploymentsForPlan = /*#__PURE__*/ query<DeploymentsDeployment>(op_deployments_forPlan);
