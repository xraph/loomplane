import type { OperationMeta } from '../ops';
import { codec_LocationPolicy } from '../codecs/LocationPolicy';
import { codec_LocationPolicyBody } from '../codecs/LocationPolicyBody';

export const op_cloudLocationPolicy_save = {
  method: 'PUT',
  path: '/api/v1/clouds/{cloudId}/location-policy',
  provides: [],
  invalidates: [],
  bodyCodec: codec_LocationPolicyBody,
  responseCodec: codec_LocationPolicy,
} as const satisfies OperationMeta;
