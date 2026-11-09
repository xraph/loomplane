import type { OperationMeta } from '../ops';
import { codec_HostInputBody } from '../codecs/HostInputBody';
import { codec_NewHost } from '../codecs/NewHost';

export const op_platformCloudHosts_create = {
  method: 'POST',
  path: '/api/v1/platform/cloud/hosts',
  rootType: 'NewHost',
  provides: [],
  invalidates: [],
  bodyCodec: codec_HostInputBody,
  responseCodec: codec_NewHost,
} as const satisfies OperationMeta;
