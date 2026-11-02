import type { OperationMeta } from '../ops';
import { codec_NativeRetirement } from '../codecs/NativeRetirement';
import { codec_NativeRetirementInputBody } from '../codecs/NativeRetirementInputBody';

export const op_nativeRecoveryRetirements_create = {
  method: 'POST',
  path: '/api/v1/native-recovery-sources/{id}/points/{pointId}/retirements',
  entity: 'NativeRetirement',
  rootType: 'NativeRetirement',
  provides: ['NativeRetirement:{id}'],
  invalidates: ['NativeRetirement[]'],
  bodyCodec: codec_NativeRetirementInputBody,
  responseCodec: codec_NativeRetirement,
} as const satisfies OperationMeta;
