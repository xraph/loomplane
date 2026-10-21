import { query } from '@forge-go/client-core';
import { op_nativeRecoverySources_list } from '../ops/nativeRecoverySources.list';
import type { NativeRecoverySources } from '../types';

export const useNativeRecoverySourcesList = /*#__PURE__*/ query<NativeRecoverySources>(op_nativeRecoverySources_list);
