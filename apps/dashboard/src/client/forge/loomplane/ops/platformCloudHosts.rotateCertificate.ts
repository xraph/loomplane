import type { OperationMeta } from '../ops';
import { codec_NewCertificate } from '../codecs/NewCertificate';

export const op_platformCloudHosts_rotateCertificate = {
  method: 'POST',
  path: '/api/v1/platform/cloud/hosts/{host}/rotate-certificate',
  rootType: 'NewCertificate',
  provides: [],
  invalidates: [],
  responseCodec: codec_NewCertificate,
} as const satisfies OperationMeta;
