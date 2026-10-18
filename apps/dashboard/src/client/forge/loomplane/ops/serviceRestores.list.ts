import type { OperationMeta } from '../ops';
import { codec_RestoreList } from '../codecs/RestoreList';

export const op_serviceRestores_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/service-restores',
  entity: 'Restore',
  rootType: 'RestoreList',
  provides: ['Restore:{id}', 'Restore[]'],
  invalidates: [],
  responseCodec: codec_RestoreList,
} as const satisfies OperationMeta;
