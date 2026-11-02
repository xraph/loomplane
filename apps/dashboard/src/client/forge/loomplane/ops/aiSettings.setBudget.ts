import type { OperationMeta } from '../ops';
import { codec_View } from '../codecs/View';
import { codec_WorkspaceBudgetInputBody } from '../codecs/WorkspaceBudgetInputBody';

export const op_aiSettings_setBudget = {
  method: 'PUT',
  path: '/api/v1/ai/settings/budget',
  provides: [],
  invalidates: [],
  bodyCodec: codec_WorkspaceBudgetInputBody,
  responseCodec: codec_View,
} as const satisfies OperationMeta;
