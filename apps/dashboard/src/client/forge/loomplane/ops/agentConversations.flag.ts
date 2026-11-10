import type { OperationMeta } from '../ops';
import { codec_Flag } from '../codecs/Flag';
import { codec_FlagInputBody } from '../codecs/FlagInputBody';

export const op_agentConversations_flag = {
  method: 'POST',
  path: '/api/v1/agent/conversations/{id}/turns/{task}/flag',
  entity: 'Flag',
  rootType: 'Flag',
  provides: ['Flag:{id}'],
  invalidates: ['Flag[]'],
  bodyCodec: codec_FlagInputBody,
  responseCodec: codec_Flag,
} as const satisfies OperationMeta;
