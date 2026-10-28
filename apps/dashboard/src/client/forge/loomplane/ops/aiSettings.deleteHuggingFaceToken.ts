import type { OperationMeta } from '../ops';

export const op_aiSettings_deleteHuggingFaceToken = {
  method: 'DELETE',
  path: '/api/v1/ai/settings/huggingface-token',
  provides: [],
  invalidates: [],
} as const satisfies OperationMeta;
