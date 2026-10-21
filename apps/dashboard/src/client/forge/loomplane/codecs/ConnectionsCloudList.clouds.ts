import type { Codec } from '../codec-runtime';
import { codec_CloudForm } from './CloudForm';

export const codec_ConnectionsCloudList_clouds: Codec = {"kind": "array", "items": () => codec_CloudForm};
