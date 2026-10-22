import type { OperationMeta } from '../ops';
import { codec_Profile } from '../codecs/Profile';
import { codec_SetDefaultPlacementInputBody } from '../codecs/SetDefaultPlacementInputBody';

export const op_clouds_setDefaultPlacement = {
  method: 'PUT',
  path: '/api/v1/projects/{id}/clouds/{cloudId}/default-placement',
  entity: 'Profile',
  rootType: 'Profile',
  provides: ['Profile:{id}'],
  invalidates: ['Profile[]'],
  bodyCodec: codec_SetDefaultPlacementInputBody,
  responseCodec: codec_Profile,
} as const satisfies OperationMeta;
