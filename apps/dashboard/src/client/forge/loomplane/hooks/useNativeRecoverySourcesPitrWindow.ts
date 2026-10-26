import { query } from '@forge-go/client-core';
import { op_nativeRecoverySources_pitrWindow } from '../ops/nativeRecoverySources.pitrWindow';
import type { NativePITRWindow } from '../types';

export const useNativeRecoverySourcesPitrWindow = /*#__PURE__*/ query<NativePITRWindow>(op_nativeRecoverySources_pitrWindow);
