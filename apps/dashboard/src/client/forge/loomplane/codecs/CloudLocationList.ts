import type { Codec } from '../codec-runtime';
import { codec_CloudLocationList_locations } from './CloudLocationList.locations';

export const codec_CloudLocationList: Codec = {"kind": "object", "fields": {"locations": {"ts": "locations", "codec": () => codec_CloudLocationList_locations}}, "required": ["locations"]};
