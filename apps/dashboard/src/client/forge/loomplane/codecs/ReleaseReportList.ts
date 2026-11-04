import type { Codec } from '../codec-runtime';
import { codec_ReleaseReportList_reports } from './ReleaseReportList.reports';

export const codec_ReleaseReportList: Codec = {"kind": "object", "fields": {"reports": {"ts": "reports", "codec": () => codec_ReleaseReportList_reports}}, "required": ["reports"]};
