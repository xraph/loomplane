import type { OperationMeta } from '../ops';
import { codec_Zone } from '../codecs/Zone';

export const op_dnsZones_verify = {
  method: 'POST',
  path: '/api/v1/dns-zones/{zoneId}/verify',
  entity: 'Zone',
  rootType: 'Zone',
  provides: ['Zone:{id}'],
  invalidates: ['Zone[]'],
  responseCodec: codec_Zone,
} as const satisfies OperationMeta;
