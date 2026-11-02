import { query } from '@forge-go/client-core';
import { op_nativeRecoveryRetirements_list } from '../ops/nativeRecoveryRetirements.list';
import type { NativeRetirementPage } from '../types';

export const useNativeRecoveryRetirementsList = /*#__PURE__*/ query<NativeRetirementPage>(op_nativeRecoveryRetirements_list);
