import { mutation } from '@forge-go/client-core';
import { op_nativeRecoveryRetirements_retry } from '../ops/nativeRecoveryRetirements.retry';
import type { NativeRetirement } from '../types';

export const useNativeRecoveryRetirementsRetry = /*#__PURE__*/ mutation<NativeRetirement, NativeRetirement>(op_nativeRecoveryRetirements_retry);
