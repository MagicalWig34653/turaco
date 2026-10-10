import { useMemo } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useSession } from '../../platform/session/SessionProvider';
import { peopleAdminApi } from './adminApi';
import { pathLabel } from './adminModel';
import type { DepartmentNode, LocationNode } from './adminTypes';

/** Departments and Locations of the installation, for names instead of identifiers. */
export function useOrgLookups() {
  const { can } = useSession();
  const allowed = can('organization.view');
  const departments = useAsync(
    (signal): Promise<DepartmentNode[]> =>
      allowed ? peopleAdminApi.departments(signal) : Promise.resolve([]),
    [allowed],
  );
  const locations = useAsync(
    (signal): Promise<LocationNode[]> =>
      allowed ? peopleAdminApi.locations(signal) : Promise.resolve([]),
    [allowed],
  );
  return useMemo(() => {
    const departmentItems = departments.data ?? [];
    const locationItems = locations.data ?? [];
    return {
      departments: departmentItems,
      locations: locationItems,
      loading: departments.loading || locations.loading,
      departmentName: (id: string | null | undefined) =>
        pathLabel(departmentItems, id) || undefined,
      locationName: (id: string | null | undefined) => pathLabel(locationItems, id) || undefined,
    };
  }, [departments.data, locations.data, departments.loading, locations.loading]);
}
