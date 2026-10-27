import type { OperationMeta } from '../ops';
import { codec_Board } from '../codecs/Board';

export const op_octopus_board = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/board',
  provides: [],
  invalidates: [],
  responseCodec: codec_Board,
} as const satisfies OperationMeta;
