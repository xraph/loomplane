import type { OperationMeta } from '../ops';
import { codec_CloudprofilesCreateInputBody } from '../codecs/CloudprofilesCreateInputBody';
import { codec_Profile } from '../codecs/Profile';

export const op_clouds_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/clouds',
  entity: 'Profile',
  rootType: 'Profile',
  provides: ['Profile:{id}'],
  invalidates: ['Profile[]'],
  bodyCodec: codec_CloudprofilesCreateInputBody,
  responseCodec: codec_Profile,
} as const satisfies OperationMeta;
