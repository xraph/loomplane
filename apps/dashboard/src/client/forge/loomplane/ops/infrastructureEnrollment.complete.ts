import type { OperationMeta } from '../ops';
import { codec_EnrollmentBody } from '../codecs/EnrollmentBody';

export const op_infrastructureEnrollment_complete = {
  method: 'POST',
  path: '/api/v1/infrastructure/enroll/{orgId}/{operationId}',
  provides: [],
  invalidates: [],
  bodyCodec: codec_EnrollmentBody,
} as const satisfies OperationMeta;
