import type { OperationMeta } from '../ops';
import { codec_DefaultCloudInputBody } from '../codecs/DefaultCloudInputBody';
import { codec_Preview } from '../codecs/Preview';

export const op_defaultCloud_preview = {
  method: 'POST',
  path: '/api/v1/projects/{id}/default-cloud/preview',
  entity: 'Preview',
  rootType: 'Preview',
  provides: ['Preview:{id}'],
  invalidates: ['Preview[]'],
  bodyCodec: codec_DefaultCloudInputBody,
  responseCodec: codec_Preview,
} as const satisfies OperationMeta;
