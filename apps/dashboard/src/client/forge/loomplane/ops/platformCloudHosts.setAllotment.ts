import type { OperationMeta } from '../ops';
import { codec_AllotmentBody } from '../codecs/AllotmentBody';
import { codec_Host } from '../codecs/Host';

export const op_platformCloudHosts_setAllotment = {
  method: 'PUT',
  path: '/api/v1/platform/cloud/hosts/{host}/allotment',
  entity: 'Host',
  rootType: 'Host',
  provides: ['Host:{id}'],
  invalidates: ['Host[]'],
  bodyCodec: codec_AllotmentBody,
  responseCodec: codec_Host,
} as const satisfies OperationMeta;
