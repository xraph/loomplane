import type { Codec } from '../codec-runtime';
import { codec_HostView } from './HostView';

export const codec_HostList_hosts: Codec = {"kind": "array", "items": () => codec_HostView};
