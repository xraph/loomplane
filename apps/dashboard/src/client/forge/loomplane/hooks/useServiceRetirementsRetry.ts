import { mutation } from '@forge-go/client-core';
import { op_serviceRetirements_retry } from '../ops/serviceRetirements.retry';
import type { Retirement } from '../types';

export const useServiceRetirementsRetry = /*#__PURE__*/ mutation<Retirement, Retirement>(op_serviceRetirements_retry);
