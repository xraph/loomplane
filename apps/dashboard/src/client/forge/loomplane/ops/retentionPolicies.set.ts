import type { OperationMeta } from '../ops';
import { codec_RetentionPolicy } from '../codecs/RetentionPolicy';
import { codec_RetentionPolicyInputBody } from '../codecs/RetentionPolicyInputBody';

export const op_retentionPolicies_set = {
  method: 'PUT',
  path: '/api/v1/service-backups/{id}/retention-policy',
  entity: 'RetentionPolicy',
  rootType: 'RetentionPolicy',
  provides: ['RetentionPolicy:{id}'],
  invalidates: ['RetentionPolicy[]'],
  bodyCodec: codec_RetentionPolicyInputBody,
  responseCodec: codec_RetentionPolicy,
} as const satisfies OperationMeta;
