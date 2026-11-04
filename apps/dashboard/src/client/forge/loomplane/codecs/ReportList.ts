import type { Codec } from '../codec-runtime';
import { codec_ReportList_reports } from './ReportList.reports';

export const codec_ReportList: Codec = {"kind": "object", "fields": {"reports": {"ts": "reports", "codec": () => codec_ReportList_reports}}, "required": ["reports"]};
