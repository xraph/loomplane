import type { OperationMeta } from '../ops';
import { codec_FleetOperation } from '../codecs/FleetOperation';

export const op_fleetInstances_activeOperation = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/instances/{id}/active-operation',
  entity: 'FleetOperation',
  rootType: 'FleetOperation',
  provides: ['FleetOperation:{id}'],
  invalidates: [],
  responseCodec: codec_FleetOperation,
} as const satisfies OperationMeta;
