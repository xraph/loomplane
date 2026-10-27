import type { Codec } from '../codec-runtime';
import { codec_ServerImage_accelerators } from './ServerImage.accelerators';
import { codec_ServerImage_regions } from './ServerImage.regions';

export const codec_ServerImage: Codec = {"kind": "object", "fields": {"accelerators": {"ts": "accelerators", "codec": () => codec_ServerImage_accelerators}, "architecture": {"ts": "architecture"}, "bootstrap": {"ts": "bootstrap"}, "id": {"ts": "id"}, "min_disk_gb": {"ts": "minDiskGb"}, "name": {"ts": "name"}, "regions": {"ts": "regions", "codec": () => codec_ServerImage_regions}}, "required": ["bootstrap", "id", "name", "regions"]};
