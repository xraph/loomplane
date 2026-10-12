import type { OperationMeta } from '../ops';
import { codec_AddSourceInputBody } from '../codecs/AddSourceInputBody';
import { codec_Source } from '../codecs/Source';

export const op_sources_create = {
  method: 'POST',
  path: '/api/v1/projects/{id}/sources',
  entity: 'Source',
  rootType: 'Source',
  provides: ['Source:{id}'],
  invalidates: ['Source[]'],
  bodyCodec: codec_AddSourceInputBody,
  responseCodec: codec_Source,
} as const satisfies OperationMeta;
