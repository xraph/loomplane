import type { OperationMeta } from '../ops';
import { codec_NativeRestore } from '../codecs/NativeRestore';

export const op_nativeRecoveryCopies_cleanup = {
  method: 'DELETE',
  path: '/api/v1/native-recovery-copies/{id}',
  entity: 'NativeRestore',
  rootType: 'NativeRestore',
  provides: [],
  invalidates: ['NativeRestore[]'],
  responseCodec: codec_NativeRestore,
} as const satisfies OperationMeta;
