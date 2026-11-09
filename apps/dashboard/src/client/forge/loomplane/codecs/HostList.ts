import type { Codec } from '../codec-runtime';
import { codec_HostList_hosts } from './HostList.hosts';

export const codec_HostList: Codec = {"kind": "object", "fields": {"hosts": {"ts": "hosts", "codec": () => codec_HostList_hosts}}, "required": ["hosts"]};
