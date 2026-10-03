// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type Manufacturer = {
  id: string;
  name: string;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type ProductCategory = Manufacturer & { parentId?: string };

export type Product = {
  id: string;
  name: string;
  manufacturerId: string | null;
  categoryId: string | null;
  manufacturerPartNumber: string | null;
  internalPartNumber: string | null;
  serialized: boolean;
  stockManaged: boolean;
  assetManaged: boolean;
  active: boolean;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type ProductFilter = {
  q?: string;
  categoryId?: string;
  manufacturerId?: string;
  active?: boolean;
};

export type ProductBody = {
  name: string;
  manufacturerId?: string | null;
  categoryId?: string | null;
  manufacturerPartNumber?: string;
  internalPartNumber?: string;
  serialized?: boolean;
  stockManaged?: boolean;
  assetManaged?: boolean;
};

export type ProductUpdateBody = {
  expectedVersion: number;
  name?: string;
  manufacturerId?: string;
  clearManufacturer?: boolean;
  categoryId?: string;
  clearCategory?: boolean;
  manufacturerPartNumber?: string;
  internalPartNumber?: string;
  serialized?: boolean;
  stockManaged?: boolean;
  assetManaged?: boolean;
};
