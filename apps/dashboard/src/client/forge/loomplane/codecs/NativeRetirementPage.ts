import type { Codec } from '../codec-runtime';
import { codec_NativeRetirementPage_retirements } from './NativeRetirementPage.retirements';

export const codec_NativeRetirementPage: Codec = {"kind": "object", "fields": {"next_before": {"ts": "nextBefore"}, "retirements": {"ts": "retirements", "codec": () => codec_NativeRetirementPage_retirements}}, "required": ["retirements"]};
