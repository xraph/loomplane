import { query } from '@forge-go/client-core';
import { op_deployments_get } from '../ops/deployments.get';
import type { DeploymentsDeployment } from '../types';

export const useDeploymentsGet = /*#__PURE__*/ query<DeploymentsDeployment>(op_deployments_get);
