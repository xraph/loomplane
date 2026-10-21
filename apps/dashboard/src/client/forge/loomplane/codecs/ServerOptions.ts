import type { Codec } from '../codec-runtime';
import { codec_ServerOptions_images } from './ServerOptions.images';
import { codec_ServerOptions_regions } from './ServerOptions.regions';
import { codec_ServerOptions_sizes } from './ServerOptions.sizes';

export const codec_ServerOptions: Codec = {"kind": "object", "fields": {"images": {"ts": "images", "codec": () => codec_ServerOptions_images}, "regions": {"ts": "regions", "codec": () => codec_ServerOptions_regions}, "sizes": {"ts": "sizes", "codec": () => codec_ServerOptions_sizes}}, "required": ["images", "regions", "sizes"]};
