import type { OperationMeta } from '../ops';
import { codec_CloudprofilesUpdateInputBody } from '../codecs/CloudprofilesUpdateInputBody';
import { codec_Profile } from '../codecs/Profile';

export const op_organizationClouds_update = {
  method: 'PUT',
  path: '/api/v1/clouds/{cloudId}',
  entity: 'Profile',
  rootType: 'Profile',
  provides: ['Profile:{id}'],
  invalidates: ['Profile[]'],
  bodyCodec: codec_CloudprofilesUpdateInputBody,
  responseCodec: codec_Profile,
} as const satisfies OperationMeta;
