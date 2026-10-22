import type { OperationMeta } from '../ops';
import { codec_ProjectZoneList } from '../codecs/ProjectZoneList';

export const op_dnsZones_project = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/dns-zones',
  entity: 'ProjectZone',
  rootType: 'ProjectZoneList',
  provides: ['ProjectZone:{id}', 'ProjectZone[]'],
  invalidates: [],
  responseCodec: codec_ProjectZoneList,
} as const satisfies OperationMeta;
