import type { OperationMeta } from '../ops';
import { codec_CheckResult } from '../codecs/CheckResult';
import { codec_ServiceCheckInputBody } from '../codecs/ServiceCheckInputBody';

export const op_catalogEndpoints_probe = {
  method: 'POST',
  path: '/api/v1/catalog/endpoints/probes',
  provides: [],
  invalidates: [],
  bodyCodec: codec_ServiceCheckInputBody,
  responseCodec: codec_CheckResult,
} as const satisfies OperationMeta;
