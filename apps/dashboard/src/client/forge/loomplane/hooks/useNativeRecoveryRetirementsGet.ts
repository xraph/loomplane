import { query } from '@forge-go/client-core';
import { op_nativeRecoveryRetirements_get } from '../ops/nativeRecoveryRetirements.get';
import type { NativeRetirement } from '../types';

export const useNativeRecoveryRetirementsGet = /*#__PURE__*/ query<NativeRetirement>(op_nativeRecoveryRetirements_get);
