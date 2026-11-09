import type { OperationMeta } from '../ops';
import { codec_Instance } from '../codecs/Instance';

export const op_fleetInstances_get = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/instances/{id}',
  entity: 'Instance',
  rootType: 'Instance',
  provides: ['Instance:{id}'],
  invalidates: [],
  responseCodec: codec_Instance,
} as const satisfies OperationMeta;
