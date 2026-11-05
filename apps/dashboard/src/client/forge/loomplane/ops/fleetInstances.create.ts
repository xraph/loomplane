import type { OperationMeta } from '../ops';
import { codec_CreateInstanceInputBody } from '../codecs/CreateInstanceInputBody';
import { codec_OperationResult } from '../codecs/OperationResult';

export const op_fleetInstances_create = {
  method: 'POST',
  path: '/api/v1/projects/{project_id}/fleet/instances',
  rootType: 'OperationResult',
  provides: [],
  invalidates: [],
  bodyCodec: codec_CreateInstanceInputBody,
  responseCodec: codec_OperationResult,
} as const satisfies OperationMeta;
