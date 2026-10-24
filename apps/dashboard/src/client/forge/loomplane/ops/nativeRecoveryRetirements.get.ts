import type { OperationMeta } from '../ops';
import { codec_NativeRetirement } from '../codecs/NativeRetirement';

export const op_nativeRecoveryRetirements_get = {
  method: 'GET',
  path: '/api/v1/native-recovery-retirements/{id}',
  entity: 'NativeRetirement',
  rootType: 'NativeRetirement',
  provides: ['NativeRetirement:{id}'],
  invalidates: [],
  responseCodec: codec_NativeRetirement,
} as const satisfies OperationMeta;
