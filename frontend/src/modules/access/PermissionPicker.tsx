import { useMemo, useState } from 'react';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useOptionalText } from '../../platform/i18n/optionalText';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import {
  filterPermissions,
  grantability,
  groupByModule,
  missingNeeds,
  riskCounts,
  type Actor,
  type PickerFilter,
} from './accessModel';
import type { Permission, PermissionRisk } from './types';

const riskKey: Record<PermissionRisk, MessageKey> = {
  normal: 'risk.normal',
  elevated: 'risk.elevated',
  high: 'risk.high',
};
const riskTone = { normal: 'neutral', elevated: 'warning', high: 'danger' } as const;

export function RiskBadge({ risk }: { risk: PermissionRisk }) {
  const { t } = useI18n();
  return <Badge tone={riskTone[risk]}>{t(riskKey[risk])}</Badge>;
}

export function useModuleName() {
  const text = useOptionalText();
  return (module: string) => text(`access.module.${module}`, module);
}

type Props = {
  permissions: readonly Permission[];
  selected: ReadonlySet<string>;
  onChange: (next: Set<string>) => void;
  /** Read-only view (built-in Role, a template preview or missing manage permission). */
  disabled?: boolean;
  actor: Actor;
  moduleEnabled: (module: string) => boolean;
  /** Start with only the selected permissions listed (previews of a template or a built-in Role). */
  initialOnlySelected?: boolean;
};

/**
 * Grouped, searchable permission picker. Modules are collapsible groups with selected counts; every row
 * shows its risk as text, the companion permissions it needs and, when the signed-in person may not
 * grant it, why the checkbox is disabled. The server enforces all of it.
 */
export function PermissionPicker({
  permissions,
  selected,
  onChange,
  disabled,
  actor,
  moduleEnabled,
  initialOnlySelected,
}: Props) {
  const { t } = useI18n();
  const moduleName = useModuleName();
  const [filter, setFilter] = useState<PickerFilter>({
    search: '',
    risk: '',
    onlySelected: initialOnlySelected ?? false,
  });
  const byName = useMemo(
    () => new Map(permissions.map((permission) => [permission.name, permission])),
    [permissions],
  );
  const visible = useMemo(
    () => filterPermissions(permissions, filter, selected),
    [permissions, filter, selected],
  );
  const modules = useMemo(() => groupByModule(visible), [visible]);
  const counts = riskCounts(selected, byName);
  const missing = missingNeeds(selected, byName);
  const searching = filter.search.trim() !== '' || filter.risk !== '' || filter.onlySelected;

  const change = (names: readonly string[], on: boolean) => {
    const next = new Set(selected);
    for (const name of names) {
      if (on) next.add(name);
      else next.delete(name);
    }
    onChange(next);
  };
  const canAdd = (permission: Permission) => grantability(permission, actor) === 'ok';

  return (
    <div className="adm-picker">
      <div className="adm-picker-bar" role="search" aria-label={t('picker.filters')}>
        <TextField
          label={t('picker.search')}
          type="search"
          value={filter.search}
          maxLength={100}
          onChange={(event) => setFilter({ ...filter, search: event.target.value })}
        />
        <Select
          label={t('picker.risk')}
          value={filter.risk}
          onChange={(event) =>
            setFilter({ ...filter, risk: event.target.value as PickerFilter['risk'] })
          }
          options={[
            { value: '', label: t('picker.risk.all') },
            { value: 'normal', label: t('risk.normal') },
            { value: 'elevated', label: t('risk.elevated') },
            { value: 'high', label: t('risk.high') },
          ]}
        />
        <Checkbox
          label={t('picker.onlySelected')}
          checked={filter.onlySelected}
          onChange={(event) => setFilter({ ...filter, onlySelected: event.target.checked })}
        />
      </div>
      <p className="adm-picker-summary" role="status">
        {t('picker.summary', {
          count: selected.size,
          elevated: counts.elevated,
          high: counts.high,
        })}
      </p>
      {missing.length > 0 ? (
        <div className="alert alert-warning" role="status">
          <p>
            <strong>{t('picker.needs.title')}</strong>
          </p>
          <ul>
            {missing.map((entry) => (
              <li key={entry.permission}>
                {t('picker.needs.line', {
                  permission: entry.permission,
                  needs: entry.needs.join(', '),
                })}{' '}
                {!disabled ? (
                  <Button
                    onClick={() =>
                      change(
                        entry.needs.filter(
                          (name) => byName.has(name) && canAdd(byName.get(name) as Permission),
                        ),
                        true,
                      )
                    }
                  >
                    {t('picker.needs.add')}
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {modules.length === 0 ? <p className="empty">{t('picker.empty')}</p> : null}
      {modules.map((bucket) => {
        const selectedHere = bucket.items.filter((item) => selected.has(item.name)).length;
        const off = !moduleEnabled(bucket.module);
        return (
          <details
            key={bucket.module}
            className={`adm-module${off ? ' adm-module-off' : ''}`}
            open={searching || selectedHere > 0 ? true : undefined}
          >
            <summary>
              <span className="adm-module-name">{moduleName(bucket.module)}</span>
              <span className="adm-module-count">
                {t('picker.moduleCount', { selected: selectedHere, total: bucket.items.length })}
              </span>
              {off ? <Badge tone="unknown">{t('picker.moduleOff')}</Badge> : null}
            </summary>
            {bucket.groups.map((group) => {
              const addable = group.items.filter(canAdd);
              const allOn = addable.length > 0 && addable.every((item) => selected.has(item.name));
              return (
                <fieldset key={group.group} className="adm-group">
                  <legend>
                    {t(`picker.group.${group.group}`)}
                    {!disabled && addable.length > 0 ? (
                      <button
                        type="button"
                        className="btn-link adm-group-toggle"
                        onClick={() =>
                          change(
                            addable.map((item) => item.name),
                            !allOn,
                          )
                        }
                      >
                        {allOn ? t('picker.group.clear') : t('picker.group.selectAll')}
                      </button>
                    ) : null}
                  </legend>
                  {group.items.map((permission) => {
                    const ceiling = grantability(permission, actor);
                    const on = selected.has(permission.name);
                    return (
                      <div key={permission.name} className="adm-permission">
                        <Checkbox
                          checked={on}
                          disabled={disabled || (ceiling !== 'ok' && !on)}
                          onChange={(event) => change([permission.name], event.target.checked)}
                          label={
                            <>
                              <code>{permission.name}</code> <RiskBadge risk={permission.risk} />
                              {ceiling !== 'ok' ? (
                                <Badge tone="unknown">
                                  {t(
                                    ceiling === 'beyondOwn'
                                      ? 'picker.ceiling.beyondOwn'
                                      : 'picker.ceiling.administratorOnly',
                                  )}
                                </Badge>
                              ) : null}
                            </>
                          }
                          description={
                            <>
                              {permission.description}
                              {permission.needs && permission.needs.length > 0 ? (
                                <span className="adm-needs">
                                  {' '}
                                  {t('picker.needs.label', { needs: permission.needs.join(', ') })}
                                </span>
                              ) : null}
                            </>
                          }
                        />
                      </div>
                    );
                  })}
                </fieldset>
              );
            })}
          </details>
        );
      })}
    </div>
  );
}
