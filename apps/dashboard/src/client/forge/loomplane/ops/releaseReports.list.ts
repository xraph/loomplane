import type { OperationMeta } from '../ops';
import { codec_ReleaseReportList } from '../codecs/ReleaseReportList';

export const op_releaseReports_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/release-reports',
  provides: [],
  invalidates: [],
  responseCodec: codec_ReleaseReportList,
} as const satisfies OperationMeta;
