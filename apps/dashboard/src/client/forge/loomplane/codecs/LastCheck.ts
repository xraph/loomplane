import type { Codec } from '../codec-runtime';
import { codec_Capabilities } from './Capabilities';
import { codec_LastCheck_notes } from './LastCheck.notes';
import { codec_LastCheck_problems } from './LastCheck.problems';

export const codec_LastCheck: Codec = {"kind": "object", "fields": {"admin": {"ts": "admin", "codec": () => codec_Capabilities}, "at": {"ts": "at"}, "capabilities": {"ts": "capabilities", "codec": () => codec_Capabilities}, "notes": {"ts": "notes", "codec": () => codec_LastCheck_notes}, "ok": {"ts": "ok"}, "problems": {"ts": "problems", "codec": () => codec_LastCheck_problems}, "target": {"ts": "target"}, "version": {"ts": "version"}}, "required": ["at", "ok"]};
