import type { Codec } from '../codec-runtime';
import { codec_StorageVolumeList_volumes } from './StorageVolumeList.volumes';

export const codec_StorageVolumeList: Codec = {"kind": "object", "fields": {"volumes": {"ts": "volumes", "codec": () => codec_StorageVolumeList_volumes}}, "required": ["volumes"]};
