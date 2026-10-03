import { useAsync } from '../../platform/api/useAsync';
import type { SelectOption } from '../../platform/ui/Field';
import { inventoryApi } from './api';

/** All active storage locations as "Warehouse / Storage location" options (needs inventory.view). */
export function useStorageLocationOptions() {
  const loaded = useAsync(async (signal): Promise<SelectOption[]> => {
    const warehouses = await inventoryApi.warehouses(false, signal);
    const lists = await Promise.all(
      warehouses.items.map(async (w) => {
        const locations = await inventoryApi.locations(w.id, false, signal);
        return locations.items.map((l) => ({ value: l.id, label: `${w.name} / ${l.name}` }));
      }),
    );
    return lists.flat();
  }, []);
  return loaded;
}
