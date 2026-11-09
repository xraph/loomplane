import { mutation } from '@forge-go/client-core';
import { op_releaseReports_prepare } from '../ops/releaseReports.prepare';
import type { ReleaseReport } from '../types';

export const useReleaseReportsPrepare = /*#__PURE__*/ mutation<ReleaseReport, ReleaseReport>(op_releaseReports_prepare);
