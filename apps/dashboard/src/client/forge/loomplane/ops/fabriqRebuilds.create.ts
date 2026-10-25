import type { OperationMeta } from '../ops';
import { codec_Rebuild } from '../codecs/Rebuild';
import { codec_RebuildInputBody } from '../codecs/RebuildInputBody';

export const op_fabriqRebuilds_create = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/rebuilds',
  entity: 'Rebuild',
  rootType: 'Rebuild',
  provides: ['Rebuild:{id}'],
  invalidates: ['Rebuild[]'],
  bodyCodec: codec_RebuildInputBody,
  responseCodec: codec_Rebuild,
} as const satisfies OperationMeta;
