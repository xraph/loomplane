import { query } from '@forge-go/client-core';
import { op_deployments_canary } from '../ops/deployments.canary';
import type { CanaryReport } from '../types';

export const useDeploymentsCanary = /*#__PURE__*/ query<CanaryReport>(op_deployments_canary);
