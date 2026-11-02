import type { Codec } from '../codec-runtime';
import { codec_Board_hosts } from './Board.hosts';
import { codec_Board_unroutable } from './Board.unroutable';
import { codec_Board_unrouted } from './Board.unrouted';
import { codec_DiscoverySummary } from './DiscoverySummary';
import { codec_OctopusStatus } from './OctopusStatus';
import { codec_SourceCounts } from './SourceCounts';

export const codec_Board: Codec = {"kind": "object", "fields": {"counts": {"ts": "counts", "codec": () => codec_SourceCounts}, "discovery": {"ts": "discovery", "codec": () => codec_DiscoverySummary}, "gateway": {"ts": "gateway", "codec": () => codec_OctopusStatus}, "hosts": {"ts": "hosts", "codec": () => codec_Board_hosts}, "live": {"ts": "live"}, "live_error": {"ts": "liveError"}, "unroutable": {"ts": "unroutable", "codec": () => codec_Board_unroutable}, "unrouted": {"ts": "unrouted", "codec": () => codec_Board_unrouted}}, "required": ["counts", "discovery", "gateway", "hosts", "live", "unroutable", "unrouted"]};
