import type { OperationMeta } from '../ops';
import { codec_ProviderZoneList } from '../codecs/ProviderZoneList';

export const op_dnsZones_discover = {
  method: 'GET',
  path: '/api/v1/dns-zones/discover',
  entity: 'DNSZone',
  rootType: 'ProviderZoneList',
  provides: ['DNSZone:{id}', 'DNSZone[]'],
  invalidates: [],
  responseCodec: codec_ProviderZoneList,
} as const satisfies OperationMeta;
