import type { Codec } from '../codec-runtime';
import { codec_Report } from './Report';

export const codec_ReportList_reports: Codec = {"kind": "array", "items": () => codec_Report};
