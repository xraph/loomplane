import { query } from '@forge-go/client-core';
import { op_nativeRecoveryRetirements_history } from '../ops/nativeRecoveryRetirements.history';
import type { RetirementHistory } from '../types';

export const useNativeRecoveryRetirementsHistory = /*#__PURE__*/ query<RetirementHistory>(op_nativeRecoveryRetirements_history);
