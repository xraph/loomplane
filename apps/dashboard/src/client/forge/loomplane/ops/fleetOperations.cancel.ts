import type { OperationMeta } from '../ops';
import { codec_FleetCancelOperationInputBody } from '../codecs/FleetCancelOperationInputBody';
import { codec_FleetOperation } from '../codecs/FleetOperation';

export const op_fleetOperations_cancel = {
  method: 'POST',
  path: '/api/v1/projects/{project_id}/fleet/operations/{id}/cancel',
  entity: 'FleetOperation',
  rootType: 'FleetOperation',
  provides: ['FleetOperation:{id}'],
  invalidates: ['FleetOperation[]'],
  bodyCodec: codec_FleetCancelOperationInputBody,
  responseCodec: codec_FleetOperation,
} as const satisfies OperationMeta;
