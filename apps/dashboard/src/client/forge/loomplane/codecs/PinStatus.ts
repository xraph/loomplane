import type { Codec } from '../codec-runtime';

export const codec_PinStatus: Codec = {"kind": "object", "fields": {"file": {"ts": "file"}, "message": {"ts": "message"}, "model": {"ts": "model"}, "revision": {"ts": "revision"}, "update_available": {"ts": "updateAvailable"}}, "required": ["update_available"]};
