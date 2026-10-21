import { query } from '@forge-go/client-core';
import { op_serviceRetirements_history } from '../ops/serviceRetirements.history';
import type { RetirementHistory } from '../types';

export const useServiceRetirementsHistory = /*#__PURE__*/ query<RetirementHistory>(op_serviceRetirements_history);
