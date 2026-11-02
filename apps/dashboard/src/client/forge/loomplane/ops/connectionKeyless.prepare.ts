import type { OperationMeta } from '../ops';
import { codec_KeylessPrepareInputBody } from '../codecs/KeylessPrepareInputBody';
import { codec_KeylessSetup } from '../codecs/KeylessSetup';

export const op_connectionKeyless_prepare = {
  method: 'POST',
  path: '/api/v1/connections/keyless/prepare',
  provides: [],
  invalidates: [],
  bodyCodec: codec_KeylessPrepareInputBody,
  responseCodec: codec_KeylessSetup,
} as const satisfies OperationMeta;
