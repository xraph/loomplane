import type { OperationMeta } from '../ops';
import { codec_RetentionPolicyHistory } from '../codecs/RetentionPolicyHistory';

export const op_retentionPolicies_history = {
  method: 'GET',
  path: '/api/v1/service-backups/{id}/retention-policy/history',
  provides: [],
  invalidates: [],
  responseCodec: codec_RetentionPolicyHistory,
} as const satisfies OperationMeta;
