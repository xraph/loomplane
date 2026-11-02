import type { OperationMeta } from '../ops';
import { codec_DomainDetail } from '../codecs/DomainDetail';

export const op_dnsZones_get = {
  method: 'GET',
  path: '/api/v1/dns-zones/{zoneId}',
  rootType: 'DomainDetail',
  provides: [],
  invalidates: [],
  responseCodec: codec_DomainDetail,
} as const satisfies OperationMeta;
