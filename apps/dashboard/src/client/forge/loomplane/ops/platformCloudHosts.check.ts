import type { OperationMeta } from '../ops';
import { codec_Host } from '../codecs/Host';

export const op_platformCloudHosts_check = {
  method: 'POST',
  path: '/api/v1/platform/cloud/hosts/{host}/check',
  entity: 'Host',
  rootType: 'Host',
  provides: ['Host:{id}'],
  invalidates: ['Host[]'],
  responseCodec: codec_Host,
} as const satisfies OperationMeta;
