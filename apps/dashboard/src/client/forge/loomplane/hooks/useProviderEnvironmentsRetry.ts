import { mutation } from '@forge-go/client-core';
import { op_providerEnvironments_retry } from '../ops/providerEnvironments.retry';
import type { Plan } from '../types';

export const useProviderEnvironmentsRetry = /*#__PURE__*/ mutation<Plan, Plan>(op_providerEnvironments_retry);
