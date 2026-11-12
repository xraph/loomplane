import type { OperationMeta } from '../ops';
import { codec_ActionInputBody } from '../codecs/ActionInputBody';
import { codec_FleetOperation } from '../codecs/FleetOperation';

export const op_fleetInstances_requestAction = {
  method: 'POST',
  path: '/api/v1/projects/{project_id}/fleet/instances/{id}/operations',
  entity: 'FleetOperation',
  rootType: 'FleetOperation',
  provides: ['FleetOperation:{id}'],
  invalidates: ['FleetOperation[]'],
  bodyCodec: codec_ActionInputBody,
  responseCodec: codec_FleetOperation,
} as const satisfies OperationMeta;
