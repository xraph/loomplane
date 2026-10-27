import type { Codec } from '../codec-runtime';
import { codec_KeylessState } from './KeylessState';
import { codec_SignInAppList_apps } from './SignInAppList.apps';

export const codec_SignInAppList: Codec = {"kind": "object", "fields": {"apps": {"ts": "apps", "codec": () => codec_SignInAppList_apps}, "callback_url": {"ts": "callbackUrl"}, "keyless": {"ts": "keyless", "codec": () => codec_KeylessState}}, "required": ["apps", "callback_url", "keyless"]};
