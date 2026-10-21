import type { OperationMeta } from '../ops';
import { codec_ChangeInputBody } from '../codecs/ChangeInputBody';
import { codec_Preview } from '../codecs/Preview';

export const op_organizationCloudChanges_preview = {
  method: 'POST',
  path: '/api/v1/clouds/changes/preview',
  entity: 'Preview',
  rootType: 'Preview',
  provides: ['Preview:{id}'],
  invalidates: ['Preview[]'],
  bodyCodec: codec_ChangeInputBody,
  responseCodec: codec_Preview,
} as const satisfies OperationMeta;
