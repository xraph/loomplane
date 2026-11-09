import type { OperationMeta } from '../ops';
import { codec_FleetOperation } from '../codecs/FleetOperation';

export const op_fleetOperations_get = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/operations/{id}',
  entity: 'FleetOperation',
  rootType: 'FleetOperation',
  provides: ['FleetOperation:{id}'],
  invalidates: [],
  responseCodec: codec_FleetOperation,
} as const satisfies OperationMeta;
