import type { OperationMeta } from '../ops';
import { codec_RetentionPolicyState } from '../codecs/RetentionPolicyState';

export const op_retentionPolicies_get = {
  method: 'GET',
  path: '/api/v1/service-backups/{id}/retention-policy',
  rootType: 'RetentionPolicyState',
  provides: [],
  invalidates: [],
  responseCodec: codec_RetentionPolicyState,
} as const satisfies OperationMeta;
