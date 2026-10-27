import type { Codec } from '../codec-runtime';
import { codec_SpendReport_accounts } from './SpendReport.accounts';
import { codec_SpendReport_sources } from './SpendReport.sources';
import { codec_SpendReport_totals } from './SpendReport.totals';

export const codec_SpendReport: Codec = {"kind": "object", "fields": {"accounts": {"ts": "accounts", "codec": () => codec_SpendReport_accounts}, "month": {"ts": "month"}, "sources": {"ts": "sources", "codec": () => codec_SpendReport_sources}, "totals": {"ts": "totals", "codec": () => codec_SpendReport_totals}}, "required": ["accounts", "month", "sources", "totals"]};
