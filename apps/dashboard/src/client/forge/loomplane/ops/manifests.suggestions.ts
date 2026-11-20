import type { OperationMeta } from '../ops';
import { codec_ServiceSuggestions } from '../codecs/ServiceSuggestions';

export const op_manifests_suggestions = {
  method: 'GET',
  path: '/api/v1/projects/{id}/manifest/suggestions',
  entity: 'ServiceSuggestions',
  rootType: 'ServiceSuggestions',
  provides: ['ServiceSuggestions:{projectId}'],
  invalidates: [],
  responseCodec: codec_ServiceSuggestions,
} as const satisfies OperationMeta;
