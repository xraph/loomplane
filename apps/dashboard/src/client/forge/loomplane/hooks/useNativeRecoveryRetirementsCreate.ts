import { mutation } from '@forge-go/client-core';
import { op_nativeRecoveryRetirements_create } from '../ops/nativeRecoveryRetirements.create';
import type { NativeRetirement } from '../types';

export const useNativeRecoveryRetirementsCreate = /*#__PURE__*/ mutation<NativeRetirement, NativeRetirement>(op_nativeRecoveryRetirements_create);
