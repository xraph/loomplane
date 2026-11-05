import type { OperationMeta } from '../ops';
import { codec_Fleet } from '../codecs/Fleet';

export const op_fleets_get = {
  method: 'GET',
  path: '/api/v1/projects/{project_id}/fleet',
  entity: 'Fleet',
  rootType: 'Fleet',
  provides: ['Fleet:{id}'],
  invalidates: [],
  responseCodec: codec_Fleet,
} as const satisfies OperationMeta;
