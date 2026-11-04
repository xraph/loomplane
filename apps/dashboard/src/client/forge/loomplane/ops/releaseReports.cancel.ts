import type { OperationMeta } from '../ops';
import { codec_ReleaseReport } from '../codecs/ReleaseReport';

export const op_releaseReports_cancel = {
  method: 'POST',
  path: '/api/v1/release-reports/{id}/cancel',
  entity: 'ReleaseReport',
  rootType: 'ReleaseReport',
  provides: ['ReleaseReport:{id}'],
  invalidates: ['ReleaseReport[]'],
  responseCodec: codec_ReleaseReport,
} as const satisfies OperationMeta;
