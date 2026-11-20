import type { OperationMeta } from '../ops';
import { codec_CloudSyncList } from '../codecs/CloudSyncList';

export const op_cloudSyncs_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/cloud-syncs',
  entity: 'Sync',
  rootType: 'CloudSyncList',
  provides: ['Sync:{id}', 'Sync[]'],
  invalidates: [],
  responseCodec: codec_CloudSyncList,
} as const satisfies OperationMeta;
