import { query } from '@forge-go/client-core';
import { op_deployments_get } from '../ops/deployments.get';
import type { Deployment } from '../types';

export const useDeploymentsGet = /*#__PURE__*/ query<Deployment>(op_deployments_get);
