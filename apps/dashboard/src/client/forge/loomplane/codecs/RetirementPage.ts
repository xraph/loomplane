import type { Codec } from '../codec-runtime';
import { codec_RetirementPage_retirements } from './RetirementPage.retirements';

export const codec_RetirementPage: Codec = {"kind": "object", "fields": {"next_before": {"ts": "nextBefore"}, "retirements": {"ts": "retirements", "codec": () => codec_RetirementPage_retirements}}, "required": ["retirements"]};
