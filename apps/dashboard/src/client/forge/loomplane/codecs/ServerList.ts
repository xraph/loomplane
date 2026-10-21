import type { Codec } from '../codec-runtime';
import { codec_ServerList_servers } from './ServerList.servers';

export const codec_ServerList: Codec = {"kind": "object", "fields": {"servers": {"ts": "servers", "codec": () => codec_ServerList_servers}}, "required": ["servers"]};
