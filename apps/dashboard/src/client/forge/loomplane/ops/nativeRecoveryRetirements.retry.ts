import type { OperationMeta } from '../ops';
import { codec_NativeRetirement } from '../codecs/NativeRetirement';

export const op_nativeRecoveryRetirements_retry = {
  method: 'POST',
  path: '/api/v1/native-recovery-retirements/{id}/retry',
  entity: 'NativeRetirement',
  rootType: 'NativeRetirement',
  provides: ['NativeRetirement:{id}'],
  invalidates: ['NativeRetirement[]'],
  responseCodec: codec_NativeRetirement,
} as const satisfies OperationMeta;
