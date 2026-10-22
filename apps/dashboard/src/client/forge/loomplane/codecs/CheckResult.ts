import type { Codec } from '../codec-runtime';
import { codec_CheckResult_missing } from './CheckResult.missing';
import { codec_CheckResult_notes } from './CheckResult.notes';
import { codec_CheckResult_problems } from './CheckResult.problems';

export const codec_CheckResult: Codec = {"kind": "object", "fields": {"missing": {"ts": "missing", "codec": () => codec_CheckResult_missing}, "notes": {"ts": "notes", "codec": () => codec_CheckResult_notes}, "ok": {"ts": "ok"}, "problems": {"ts": "problems", "codec": () => codec_CheckResult_problems}, "version": {"ts": "version"}}, "required": ["ok"]};
