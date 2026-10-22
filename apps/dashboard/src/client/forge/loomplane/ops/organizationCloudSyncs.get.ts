import type { OperationMeta } from '../ops';
import { codec_Sync } from '../codecs/Sync';

export const op_organizationCloudSyncs_get = {
  method: 'GET',
  path: '/api/v1/clouds/syncs/{syncId}',
  entity: 'Sync',
  rootType: 'Sync',
  provides: ['Sync:{id}'],
  invalidates: [],
  responseCodec: codec_Sync,
} as const satisfies OperationMeta;
