import { mutation } from '@forge-go/client-core';
import { op_deployments_retry } from '../ops/deployments.retry';
import type { DeploymentsDeployment } from '../types';

export const useDeploymentsRetry = /*#__PURE__*/ mutation<DeploymentsDeployment, DeploymentsDeployment>(op_deployments_retry);
