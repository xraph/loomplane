import { query } from '@forge-go/client-core';
import { op_nativeRecoveryCopies_list } from '../ops/nativeRecoveryCopies.list';
import type { NativeRecoveryCopies } from '../types';

export const useNativeRecoveryCopiesList = /*#__PURE__*/ query<NativeRecoveryCopies>(op_nativeRecoveryCopies_list);
