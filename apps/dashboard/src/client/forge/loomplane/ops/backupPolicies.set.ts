import type { OperationMeta } from '../ops';
import { codec_Policy } from '../codecs/Policy';
import { codec_PolicyInputBody } from '../codecs/PolicyInputBody';

export const op_backupPolicies_set = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/backup-policies',
  entity: 'Policy',
  rootType: 'Policy',
  provides: ['Policy:{id}'],
  invalidates: ['Policy[]'],
  bodyCodec: codec_PolicyInputBody,
  responseCodec: codec_Policy,
} as const satisfies OperationMeta;
