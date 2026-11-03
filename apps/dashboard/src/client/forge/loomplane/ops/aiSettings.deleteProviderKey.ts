import type { OperationMeta } from '../ops';

export const op_aiSettings_deleteProviderKey = {
  method: 'DELETE',
  path: '/api/v1/ai/settings/provider-keys/{name}',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;
