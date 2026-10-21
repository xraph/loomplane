import type { Codec } from '../codec-runtime';
import { codec_ServerImage_regions } from './ServerImage.regions';

export const codec_ServerImage: Codec = {"kind": "object", "fields": {"bootstrap": {"ts": "bootstrap"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "regions": {"ts": "regions", "codec": () => codec_ServerImage_regions}}, "required": ["bootstrap", "id", "name", "regions"]};
