import { emptyState, type QueryState } from '../../platform/ui/query/filterModel';

export type PeoplePreset = 'all' | 'active' | 'local' | 'directory' | 'inactive' | 'noDepartment';

export const peoplePresets: readonly PeoplePreset[] = [
  'all',
  'active',
  'local',
  'directory',
  'inactive',
  'noDepartment',
];

/** Starting points for the Users list; they are ordinary filters the person can change afterwards. */
export function presetState(preset: PeoplePreset): QueryState {
  const condition = (field: string, op: string, value?: unknown) => ({
    type: 'condition' as const,
    field,
    op,
    ...(value === undefined ? {} : { value }),
  });
  const withRoot = (root: ReturnType<typeof condition>): QueryState => ({
    ...emptyState(),
    filter: { v: 1, root },
  });
  switch (preset) {
    case 'active':
      return withRoot(condition('status', 'equals', 'active'));
    case 'local':
      return withRoot(condition('source', 'equals', 'local'));
    case 'directory':
      return withRoot(condition('source', 'equals', 'directory'));
    case 'inactive':
      return withRoot(condition('status', 'in', ['inactive', 'departed']));
    case 'noDepartment':
      return withRoot(condition('department', 'is_empty'));
    default:
      return emptyState();
  }
}
