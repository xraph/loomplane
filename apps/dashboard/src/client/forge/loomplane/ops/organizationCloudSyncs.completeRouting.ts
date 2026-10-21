import type { OperationMeta } from '../ops';
import { codec_CompleteCloudRoutingInputBody } from '../codecs/CompleteCloudRoutingInputBody';
import { codec_Sync } from '../codecs/Sync';

export const op_organizationCloudSyncs_completeRouting = {
  method: 'POST',
  path: '/api/v1/clouds/syncs/{syncId}/routing',
  entity: 'Sync',
  rootType: 'Sync',
  provides: ['Sync:{id}'],
  invalidates: ['Sync[]'],
  bodyCodec: codec_CompleteCloudRoutingInputBody,
  responseCodec: codec_Sync,
} as const satisfies OperationMeta;
