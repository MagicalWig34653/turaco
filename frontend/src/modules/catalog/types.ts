// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type FieldType =
  'text' | 'longtext' | 'number' | 'boolean' | 'date' | 'select' | 'user' | 'product';

export type CatalogItem = {
  id: string;
  key: string;
  title: string;
  description: string;
  active: boolean;
  version: number;
  updatedAt: string;
};

export type FormField = {
  key: string;
  type: FieldType;
  label: string;
  help?: string;
  required: boolean;
  maxLength?: number;
  min?: number;
  max?: number;
  options?: Array<{ value: string; label: string }>;
  productOptions?: Array<{ id: string; name: string }>;
};

export type CatalogForm = CatalogItem & {
  allowRequestedFor: boolean;
  fields: FormField[];
  /** The full definition; present only for catalog managers. */
  definition?: unknown;
};

export type CatalogItemCreate = {
  key: string;
  title: string;
  description?: string;
  definition: unknown;
};

export type CatalogItemUpdate = {
  expectedVersion: number;
  title?: string;
  description?: string;
  definition?: unknown;
};
