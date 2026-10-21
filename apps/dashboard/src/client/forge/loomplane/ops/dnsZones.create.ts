import type { OperationMeta } from '../ops';
import { codec_Zone } from '../codecs/Zone';
import { codec_ZoneInputBody } from '../codecs/ZoneInputBody';

export const op_dnsZones_create = {
  method: 'POST',
  path: '/api/v1/dns-zones',
  entity: 'Zone',
  rootType: 'Zone',
  provides: ['Zone:{id}'],
  invalidates: ['Zone[]'],
  bodyCodec: codec_ZoneInputBody,
  responseCodec: codec_Zone,
} as const satisfies OperationMeta;
