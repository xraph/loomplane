import { query } from '@forge-go/client-core';
import { op_usage_spend } from '../ops/usage.spend';
import type { SpendReport } from '../types';

export const useUsageSpend = /*#__PURE__*/ query<SpendReport>(op_usage_spend);
