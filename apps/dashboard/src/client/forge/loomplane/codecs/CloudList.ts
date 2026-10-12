import type { Codec } from '../codec-runtime';
import { codec_CloudList_clouds } from './CloudList.clouds';

export const codec_CloudList: Codec = {"kind": "object", "fields": {"clouds": {"ts": "clouds", "codec": () => codec_CloudList_clouds}}, "required": ["clouds"]};
