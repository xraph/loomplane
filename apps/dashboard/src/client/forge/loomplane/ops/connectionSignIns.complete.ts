import type { OperationMeta } from '../ops';
import { codec_CompleteInputBody } from '../codecs/CompleteInputBody';
import { codec_Connection } from '../codecs/Connection';

export const op_connectionSignIns_complete = {
  method: 'POST',
  path: '/api/v1/connections/sign-ins/complete',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  bodyCodec: codec_CompleteInputBody,
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
