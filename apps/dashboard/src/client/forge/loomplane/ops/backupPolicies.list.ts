import type { OperationMeta } from '../ops';
import { codec_PolicyList } from '../codecs/PolicyList';

export const op_backupPolicies_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/backup-policies',
  entity: 'Policy',
  rootType: 'PolicyList',
  provides: ['Policy:{id}', 'Policy[]'],
  invalidates: [],
  responseCodec: codec_PolicyList,
} as const satisfies OperationMeta;
