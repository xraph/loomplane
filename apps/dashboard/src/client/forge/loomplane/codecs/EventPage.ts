import type { Codec } from '../codec-runtime';
import { codec_EventPage_acceptance } from './EventPage.acceptance';
import { codec_EventPage_items } from './EventPage.items';

export const codec_EventPage: Codec = {"kind": "object", "fields": {"acceptance": {"ts": "acceptance", "codec": () => codec_EventPage_acceptance}, "items": {"ts": "items", "codec": () => codec_EventPage_items}, "next_cursor": {"ts": "nextCursor"}, "replay_truncated": {"ts": "replayTruncated"}}, "required": ["acceptance", "items", "replay_truncated"]};
