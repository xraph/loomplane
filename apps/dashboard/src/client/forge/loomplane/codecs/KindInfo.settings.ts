import type { Codec } from '../codec-runtime';
import { codec_SettingInfo } from './SettingInfo';

export const codec_KindInfo_settings: Codec = {"kind": "array", "items": () => codec_SettingInfo};
