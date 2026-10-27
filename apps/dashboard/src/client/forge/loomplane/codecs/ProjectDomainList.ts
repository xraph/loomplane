import type { Codec } from '../codec-runtime';
import { codec_ProjectDomainList_domains } from './ProjectDomainList.domains';

export const codec_ProjectDomainList: Codec = {"kind": "object", "fields": {"domains": {"ts": "domains", "codec": () => codec_ProjectDomainList_domains}}, "required": ["domains"]};
