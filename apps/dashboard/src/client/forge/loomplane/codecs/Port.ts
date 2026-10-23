import type { Codec } from '../codec-runtime';

export const codec_Port: Codec = {"kind": "object", "fields": {"container": {"ts": "container"}, "control": {"ts": "control"}, "name": {"ts": "name"}, "protocol": {"ts": "protocol"}, "routable": {"ts": "routable"}}, "required": ["container", "name", "protocol"]};
