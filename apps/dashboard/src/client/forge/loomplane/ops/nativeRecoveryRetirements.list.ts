import type { OperationMeta } from '../ops';
import { codec_NativeRetirementPage } from '../codecs/NativeRetirementPage';

export const op_nativeRecoveryRetirements_list = {
  method: 'GET',
  path: '/api/v1/native-recovery-sources/{id}/retirements',
  entity: 'NativeRetirement',
  rootType: 'NativeRetirementPage',
  provides: ['NativeRetirement:{id}', 'NativeRetirement[]'],
  invalidates: [],
  responseCodec: codec_NativeRetirementPage,
} as const satisfies OperationMeta;
