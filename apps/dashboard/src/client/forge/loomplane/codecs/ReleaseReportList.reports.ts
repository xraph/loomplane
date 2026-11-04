import type { Codec } from '../codec-runtime';
import { codec_ReleaseReport } from './ReleaseReport';

export const codec_ReleaseReportList_reports: Codec = {"kind": "array", "items": () => codec_ReleaseReport};
