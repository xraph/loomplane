import type { OperationMeta } from '../ops';
import { codec_CheckInputBody } from '../codecs/CheckInputBody';
import { codec_CheckResult } from '../codecs/CheckResult';

export const op_catalogEndpoints_check = {
  method: 'POST',
  path: '/api/v1/catalog/endpoints/checks',
  provides: [],
  invalidates: [],
  bodyCodec: codec_CheckInputBody,
  responseCodec: codec_CheckResult,
} as const satisfies OperationMeta;
