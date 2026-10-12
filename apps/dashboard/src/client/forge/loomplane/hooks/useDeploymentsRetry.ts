import { mutation } from '@forge-go/client-core';
import { op_deployments_retry } from '../ops/deployments.retry';
import type { Deployment } from '../types';

export const useDeploymentsRetry = /*#__PURE__*/ mutation<Deployment, Deployment>(op_deployments_retry);
