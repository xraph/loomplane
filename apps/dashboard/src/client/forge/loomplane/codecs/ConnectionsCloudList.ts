import type { Codec } from '../codec-runtime';
import { codec_ConnectionsCloudList_clouds } from './ConnectionsCloudList.clouds';

export const codec_ConnectionsCloudList: Codec = {"kind": "object", "fields": {"clouds": {"ts": "clouds", "codec": () => codec_ConnectionsCloudList_clouds}}, "required": ["clouds"]};
