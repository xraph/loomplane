import type { OperationMeta } from '../ops';
import { codec_NativeCopyCreateInputBody } from '../codecs/NativeCopyCreateInputBody';
import { codec_NativeRestore } from '../codecs/NativeRestore';

export const op_nativeRecoveryCopies_create = {
  method: 'POST',
  path: '/api/v1/native-recovery-copies',
  entity: 'NativeRestore',
  rootType: 'NativeRestore',
  provides: ['NativeRestore:{id}'],
  invalidates: ['NativeRestore[]'],
  bodyCodec: codec_NativeCopyCreateInputBody,
  responseCodec: codec_NativeRestore,
} as const satisfies OperationMeta;
