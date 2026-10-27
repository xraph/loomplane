import type { OperationMeta } from '../ops';
import { codec_NativeRestore } from '../codecs/NativeRestore';

export const op_nativeRecoveryCopies_get = {
  method: 'GET',
  path: '/api/v1/native-recovery-copies/{id}',
  entity: 'NativeRestore',
  rootType: 'NativeRestore',
  provides: ['NativeRestore:{id}'],
  invalidates: [],
  responseCodec: codec_NativeRestore,
} as const satisfies OperationMeta;
