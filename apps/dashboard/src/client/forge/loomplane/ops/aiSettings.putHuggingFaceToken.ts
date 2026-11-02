import type { OperationMeta } from '../ops';
import { codec_HuggingFaceToken } from '../codecs/HuggingFaceToken';
import { codec_HuggingFaceTokenInputBody } from '../codecs/HuggingFaceTokenInputBody';

export const op_aiSettings_putHuggingFaceToken = {
  method: 'PUT',
  path: '/api/v1/ai/settings/huggingface-token',
  provides: [],
  invalidates: [],
  bodyCodec: codec_HuggingFaceTokenInputBody,
  responseCodec: codec_HuggingFaceToken,
} as const satisfies OperationMeta;
