import type { OperationMeta } from '../ops';
import { codec_Profile } from '../codecs/Profile';

export const op_clouds_get = {
  method: 'GET',
  path: '/api/v1/projects/{id}/clouds/{cloudId}',
  entity: 'Profile',
  rootType: 'Profile',
  provides: ['Profile:{id}'],
  invalidates: [],
  responseCodec: codec_Profile,
} as const satisfies OperationMeta;
