import type { Codec } from '../codec-runtime';
import { codec_NodeAdoption } from './NodeAdoption';

export const codec_NodeAdoptionList_adoptions: Codec = {"kind": "array", "items": () => codec_NodeAdoption};
