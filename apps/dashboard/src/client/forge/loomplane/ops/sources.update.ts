import type { OperationMeta } from '../ops';
import { codec_Source } from '../codecs/Source';
import { codec_UpdateSourceInputBody } from '../codecs/UpdateSourceInputBody';

export const op_sources_update = {
  method: 'PATCH',
  path: '/api/v1/projects/{id}/sources/{alias}',
  entity: 'Source',
  rootType: 'Source',
  provides: ['Source:{id}'],
  invalidates: ['Source[]'],
  bodyCodec: codec_UpdateSourceInputBody,
  responseCodec: codec_Source,
} as const satisfies OperationMeta;
