// Types mirror api/openapi/openapi.yaml (AdminSetting).

export type SettingType = 'bool' | 'int' | 'duration' | 'enum';

/** Duration values are integer seconds. */
export type SettingValue = boolean | number | string;

export type AdminSetting = {
  key: string;
  type: SettingType;
  default: SettingValue;
  min?: number;
  max?: number;
  options?: string[];
  description: string;
  module: string;
  notYetActive: boolean;
  sensitive?: boolean;
  value: SettingValue;
  stored: boolean;
  version: number;
  updatedAt?: string;
  updatedBy?: string;
};

export type SettingsList = { items: AdminSetting[] };
