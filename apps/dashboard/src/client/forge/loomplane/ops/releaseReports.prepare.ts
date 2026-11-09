import type { OperationMeta } from '../ops';
import { codec_PrepareInputBody } from '../codecs/PrepareInputBody';
import { codec_ReleaseReport } from '../codecs/ReleaseReport';

export const op_releaseReports_prepare = {
  method: 'POST',
  path: '/api/v1/projects/{id}/release-reports',
  entity: 'ReleaseReport',
  rootType: 'ReleaseReport',
  provides: ['ReleaseReport:{id}'],
  invalidates: ['ReleaseReport[]'],
  bodyCodec: codec_PrepareInputBody,
  responseCodec: codec_ReleaseReport,
} as const satisfies OperationMeta;
