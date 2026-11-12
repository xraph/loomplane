import type { OperationMeta } from '../ops';
import { codec_EventPage } from '../codecs/EventPage';

export const op_fleetEvents_list = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet/instances/{id}/events',
  entity: 'FleetEvent',
  rootType: 'EventPage',
  provides: ['FleetEvent:{id}', 'FleetEvent[]'],
  invalidates: [],
  responseCodec: codec_EventPage,
} as const satisfies OperationMeta;
