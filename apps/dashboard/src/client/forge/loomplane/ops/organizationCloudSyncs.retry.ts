import type { OperationMeta } from '../ops';
import { codec_Sync } from '../codecs/Sync';

export const op_organizationCloudSyncs_retry = {
  method: 'POST',
  path: '/api/v1/clouds/syncs/{syncId}/retry',
  entity: 'Sync',
  rootType: 'Sync',
  provides: ['Sync:{id}'],
  invalidates: ['Sync[]'],
  responseCodec: codec_Sync,
} as const satisfies OperationMeta;
