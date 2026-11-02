import type { OperationMeta } from '../ops';
import { codec_NativeRetentionPolicy } from '../codecs/NativeRetentionPolicy';
import { codec_RetentionPolicyInputBody } from '../codecs/RetentionPolicyInputBody';

export const op_nativeRetentionPolicies_set = {
  method: 'PUT',
  path: '/api/v1/native-recovery-sources/{id}/retention-policy',
  entity: 'NativeRetentionPolicy',
  rootType: 'NativeRetentionPolicy',
  provides: ['NativeRetentionPolicy:{id}'],
  invalidates: ['NativeRetentionPolicy[]'],
  bodyCodec: codec_RetentionPolicyInputBody,
  responseCodec: codec_NativeRetentionPolicy,
} as const satisfies OperationMeta;
