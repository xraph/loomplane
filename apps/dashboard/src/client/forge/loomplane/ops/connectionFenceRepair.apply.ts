import type { OperationMeta } from '../ops';
import { codec_FenceRepair } from '../codecs/FenceRepair';
import { codec_FenceRepairInputBody } from '../codecs/FenceRepairInputBody';

export const op_connectionFenceRepair_apply = {
  method: 'POST',
  path: '/api/v1/connections/{id}/fence-repair',
  provides: [],
  invalidates: [],
  bodyCodec: codec_FenceRepairInputBody,
  responseCodec: codec_FenceRepair,
} as const satisfies OperationMeta;
