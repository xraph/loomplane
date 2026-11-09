import type { OperationMeta } from '../ops';
import { codec_Plan } from '../codecs/Plan';

export const op_providerEnvironments_retry = {
  method: 'POST',
  path: '/api/v1/projects/{id}/environments/{environmentId}/provider-environments/retry',
  entity: 'Plan',
  rootType: 'Plan',
  provides: ['Plan:{id}'],
  invalidates: ['Plan[]'],
  responseCodec: codec_Plan,
} as const satisfies OperationMeta;
