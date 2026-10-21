import type { OperationMeta } from '../ops';
import { codec_ApplyCloudInputBody } from '../codecs/ApplyCloudInputBody';
import { codec_Sync } from '../codecs/Sync';

export const op_cloudSyncs_applyRevert = {
  method: 'POST',
  path: '/api/v1/projects/{id}/cloud-syncs/revert/apply',
  entity: 'Sync',
  rootType: 'Sync',
  provides: ['Sync:{id}'],
  invalidates: ['Sync[]'],
  bodyCodec: codec_ApplyCloudInputBody,
  responseCodec: codec_Sync,
} as const satisfies OperationMeta;
