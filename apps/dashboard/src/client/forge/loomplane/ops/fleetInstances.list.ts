import type { OperationMeta } from '../ops';
import { codec_InstancePage } from '../codecs/InstancePage';

export const op_fleetInstances_list = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/instances',
  entity: 'Instance',
  rootType: 'InstancePage',
  provides: ['Instance:{id}', 'Instance[]'],
  invalidates: [],
  responseCodec: codec_InstancePage,
} as const satisfies OperationMeta;
