import type { OperationMeta } from '../ops';
import { codec_EnvironmentCloudInputBody } from '../codecs/EnvironmentCloudInputBody';
import { codec_Preview } from '../codecs/Preview';

export const op_environmentClouds_preview = {
  method: 'POST',
  path: '/api/v1/projects/{id}/environments/{environmentId}/cloud/preview',
  entity: 'Preview',
  rootType: 'Preview',
  provides: ['Preview:{id}'],
  invalidates: ['Preview[]'],
  bodyCodec: codec_EnvironmentCloudInputBody,
  responseCodec: codec_Preview,
} as const satisfies OperationMeta;
