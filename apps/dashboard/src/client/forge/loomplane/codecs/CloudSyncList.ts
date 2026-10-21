import type { Codec } from '../codec-runtime';
import { codec_CloudSyncList_syncs } from './CloudSyncList.syncs';

export const codec_CloudSyncList: Codec = {"kind": "object", "fields": {"next": {"ts": "next"}, "syncs": {"ts": "syncs", "codec": () => codec_CloudSyncList_syncs}}, "required": ["syncs"]};
