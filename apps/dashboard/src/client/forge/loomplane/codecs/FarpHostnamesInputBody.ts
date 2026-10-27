import type { Codec } from '../codec-runtime';
import { codec_FarpHostnamesInputBody_hostnames } from './FarpHostnamesInputBody.hostnames';

export const codec_FarpHostnamesInputBody: Codec = {"kind": "object", "fields": {"hostnames": {"ts": "hostnames", "codec": () => codec_FarpHostnamesInputBody_hostnames}, "revision": {"ts": "revision"}}, "required": ["hostnames", "revision"]};
