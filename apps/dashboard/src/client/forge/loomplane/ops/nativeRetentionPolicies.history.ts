import type { OperationMeta } from '../ops';
import { codec_RetentionPolicyHistory } from '../codecs/RetentionPolicyHistory';

export const op_nativeRetentionPolicies_history = {
  method: 'GET',
  path: '/api/v1/native-recovery-sources/{id}/retention-policy/history',
  provides: [],
  invalidates: [],
  responseCodec: codec_RetentionPolicyHistory,
} as const satisfies OperationMeta;
