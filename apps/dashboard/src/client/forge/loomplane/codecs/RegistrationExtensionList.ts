import type { Codec } from '../codec-runtime';
import { codec_RegistrationExtensionList_extensions } from './RegistrationExtensionList.extensions';

export const codec_RegistrationExtensionList: Codec = {"kind": "object", "fields": {"extensions": {"ts": "extensions", "codec": () => codec_RegistrationExtensionList_extensions}}, "required": ["extensions"]};
