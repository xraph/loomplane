import type { OperationMeta } from '../ops';
import { codec_NativeRetentionPolicyState } from '../codecs/NativeRetentionPolicyState';

export const op_nativeRetentionPolicies_get = {
  method: 'GET',
  path: '/api/v1/native-recovery-sources/{id}/retention-policy',
  rootType: 'NativeRetentionPolicyState',
  provides: [],
  invalidates: [],
  responseCodec: codec_NativeRetentionPolicyState,
} as const satisfies OperationMeta;
