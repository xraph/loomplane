import type { OperationMeta } from '../ops';
import { codec_CloudSyncList } from '../codecs/CloudSyncList';

export const op_organizationCloudSyncs_list = {
  method: 'GET',
  path: '/api/v1/clouds/syncs',
  provides: [],
  invalidates: [],
  responseCodec: codec_CloudSyncList,
} as const satisfies OperationMeta;
