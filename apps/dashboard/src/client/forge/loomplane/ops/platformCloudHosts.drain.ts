import type { OperationMeta } from '../ops';
import { codec_Host } from '../codecs/Host';

export const op_platformCloudHosts_drain = {
  method: 'POST',
  path: '/api/v1/platform/cloud/hosts/{host}/drain',
  entity: 'Host',
  rootType: 'Host',
  provides: ['Host:{id}'],
  invalidates: ['Host[]'],
  responseCodec: codec_Host,
} as const satisfies OperationMeta;
