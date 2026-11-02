import { query } from '@forge-go/client-core';
import { op_deployments_cleanup } from '../ops/deployments.cleanup';
import type { FailedCleanup } from '../types';

export const useDeploymentsCleanup = /*#__PURE__*/ query<FailedCleanup>(op_deployments_cleanup);
