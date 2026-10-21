import type { OperationMeta } from '../ops';
import { codec_ZoneList } from '../codecs/ZoneList';

export const op_dnsZones_list = {
  method: 'GET',
  path: '/api/v1/dns-zones',
  provides: [],
  invalidates: [],
  responseCodec: codec_ZoneList,
} as const satisfies OperationMeta;
