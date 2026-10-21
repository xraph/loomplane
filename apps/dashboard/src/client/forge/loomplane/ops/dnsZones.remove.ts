import type { OperationMeta } from '../ops';
import { codec_RemoveRouteInputBody } from '../codecs/RemoveRouteInputBody';

export const op_dnsZones_remove = {
  method: 'POST',
  path: '/api/v1/dns-zones/{zoneId}/remove',
  provides: [],
  invalidates: [],
  bodyCodec: codec_RemoveRouteInputBody,
} as const satisfies OperationMeta;
