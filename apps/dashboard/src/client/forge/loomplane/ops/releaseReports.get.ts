import type { OperationMeta } from '../ops';
import { codec_ReleaseReport } from '../codecs/ReleaseReport';

export const op_releaseReports_get = {
  method: 'GET',
  path: '/api/v1/release-reports/{id}',
  entity: 'ReleaseReport',
  rootType: 'ReleaseReport',
  provides: ['ReleaseReport:{id}'],
  invalidates: [],
  responseCodec: codec_ReleaseReport,
} as const satisfies OperationMeta;
