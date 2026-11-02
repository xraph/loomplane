import type { OperationMeta } from '../ops';
import { codec_CatalogproxySession } from '../codecs/CatalogproxySession';

export const op_catalogAdmin_open = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/catalog/{resource}/admin-sessions',
  provides: [],
  invalidates: [],
  responseCodec: codec_CatalogproxySession,
} as const satisfies OperationMeta;
