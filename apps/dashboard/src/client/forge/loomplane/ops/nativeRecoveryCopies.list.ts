import type { OperationMeta } from '../ops';
import { codec_NativeRecoveryCopies } from '../codecs/NativeRecoveryCopies';

export const op_nativeRecoveryCopies_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/native-recovery-copies',
  entity: 'NativeRestore',
  rootType: 'NativeRecoveryCopies',
  provides: ['NativeRestore:{id}', 'NativeRestore[]'],
  invalidates: [],
  responseCodec: codec_NativeRecoveryCopies,
} as const satisfies OperationMeta;
