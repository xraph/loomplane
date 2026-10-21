import { mutation } from '@forge-go/client-core';
import { op_clouds_setDefaultPlacement } from '../ops/clouds.setDefaultPlacement';
import type { Profile } from '../types';

export const useCloudsSetDefaultPlacement = /*#__PURE__*/ mutation<Profile, Profile>(op_clouds_setDefaultPlacement);
