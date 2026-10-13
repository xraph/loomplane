import { query } from '@forge-go/client-core';
import { op_deployments_logs } from '../ops/deployments.logs';
import type { LogPage } from '../types';

export const useDeploymentsLogs = /*#__PURE__*/ query<LogPage>(op_deployments_logs);
