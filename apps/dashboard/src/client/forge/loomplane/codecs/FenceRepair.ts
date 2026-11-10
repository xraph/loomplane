import type { Codec } from '../codec-runtime';
import { codec_FenceRepair_running } from './FenceRepair.running';

export const codec_FenceRepair: Codec = {"kind": "object", "fields": {"daemon": {"ts": "daemon"}, "dirty": {"ts": "dirty"}, "repaired": {"ts": "repaired"}, "review": {"ts": "review"}, "running": {"ts": "running", "codec": () => codec_FenceRepair_running}}, "required": ["daemon", "dirty", "repaired", "review", "running"]};
