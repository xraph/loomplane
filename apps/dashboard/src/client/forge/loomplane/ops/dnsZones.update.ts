import type { OperationMeta } from '../ops';
import { codec_Zone } from '../codecs/Zone';
import { codec_ZoneUpdateBody } from '../codecs/ZoneUpdateBody';

export const op_dnsZones_update = {
  method: 'POST',
  path: '/api/v1/dns-zones/{zoneId}/update',
  entity: 'Zone',
  rootType: 'Zone',
  provides: ['Zone:{id}'],
  invalidates: ['Zone[]'],
  bodyCodec: codec_ZoneUpdateBody,
  responseCodec: codec_Zone,
} as const satisfies OperationMeta;
