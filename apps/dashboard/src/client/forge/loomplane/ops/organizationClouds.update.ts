import type { OperationMeta } from '../ops';
import { codec_Profile } from '../codecs/Profile';
import { codec_UpdateInputBody } from '../codecs/UpdateInputBody';

export const op_organizationClouds_update = {
  method: 'PUT',
  path: '/api/v1/clouds/{cloudId}',
  entity: 'Profile',
  rootType: 'Profile',
  provides: ['Profile:{id}'],
  invalidates: ['Profile[]'],
  bodyCodec: codec_UpdateInputBody,
  responseCodec: codec_Profile,
} as const satisfies OperationMeta;
