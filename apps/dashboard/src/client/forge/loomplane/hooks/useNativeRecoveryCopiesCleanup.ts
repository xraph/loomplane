import { mutation } from '@forge-go/client-core';
import { op_nativeRecoveryCopies_cleanup } from '../ops/nativeRecoveryCopies.cleanup';
import type { NativeRestore } from '../types';

export const useNativeRecoveryCopiesCleanup = /*#__PURE__*/ mutation<NativeRestore, NativeRestore>(op_nativeRecoveryCopies_cleanup);
