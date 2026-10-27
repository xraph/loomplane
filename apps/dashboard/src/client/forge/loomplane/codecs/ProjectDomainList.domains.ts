import type { Codec } from '../codec-runtime';
import { codec_ProjectDomain } from './ProjectDomain';

export const codec_ProjectDomainList_domains: Codec = {"kind": "array", "items": () => codec_ProjectDomain};
