import { mutation } from '@forge-go/client-core';
import { op_nativeRecoveryCopies_create } from '../ops/nativeRecoveryCopies.create';
import type { NativeRestore } from '../types';

export const useNativeRecoveryCopiesCreate = /*#__PURE__*/ mutation<NativeRestore, NativeRestore>(op_nativeRecoveryCopies_create);
