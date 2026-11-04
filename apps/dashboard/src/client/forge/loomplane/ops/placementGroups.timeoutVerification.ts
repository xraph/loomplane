import type { OperationMeta } from '../ops';
import { codec_GroupVerificationResult } from '../codecs/GroupVerificationResult';
import { codec_TimeoutVerificationInputBody } from '../codecs/TimeoutVerificationInputBody';

export const op_placementGroups_timeoutVerification = {
  method: 'POST',
  path: '/api/v1/projects/{id}/placement-groups/timeout-verifications',
  entity: 'GroupVerificationResult',
  rootType: 'GroupVerificationResult',
  provides: ['GroupVerificationResult:{id}'],
  invalidates: ['GroupVerificationResult[]'],
  bodyCodec: codec_TimeoutVerificationInputBody,
  responseCodec: codec_GroupVerificationResult,
} as const satisfies OperationMeta;
