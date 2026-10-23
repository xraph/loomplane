import { query } from '@forge-go/client-core';
import { op_nativeRecoveryCopies_get } from '../ops/nativeRecoveryCopies.get';
import type { NativeRestore } from '../types';

export const useNativeRecoveryCopiesGet = /*#__PURE__*/ query<NativeRestore>(op_nativeRecoveryCopies_get);
