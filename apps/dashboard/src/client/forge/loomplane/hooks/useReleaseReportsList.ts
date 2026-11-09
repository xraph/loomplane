import { query } from '@forge-go/client-core';
import { op_releaseReports_list } from '../ops/releaseReports.list';
import type { ReleaseReportList } from '../types';

export const useReleaseReportsList = /*#__PURE__*/ query<ReleaseReportList>(op_releaseReports_list);
