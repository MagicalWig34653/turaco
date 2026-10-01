import { groupPermissions } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Badge } from '../../platform/ui/Alert';
import { Checkbox } from '../../platform/ui/Field';
import type { Permission } from './types';

const riskKey: Record<Permission['risk'], MessageKey> = {
  normal: 'risk.normal',
  elevated: 'risk.elevated',
  high: 'risk.high',
};
const riskTone = { normal: 'neutral', elevated: 'warning', high: 'danger' } as const;

export function RiskBadge({ risk }: { risk: Permission['risk'] }) {
  const { t } = useI18n();
  return <Badge tone={riskTone[risk]}>{t(riskKey[risk])}</Badge>;
}

type Props = {
  permissions: readonly Permission[];
  selected: ReadonlySet<string>;
  onChange?: (next: Set<string>) => void;
  /** Read-only view (built-in role or missing manage permission). */
  disabled?: boolean;
};

/** Permission checklist grouped by permission prefix; the risk is shown as text, not only color. */
export function PermissionChecklist({ permissions, selected, onChange, disabled }: Props) {
  const { t } = useI18n();
  const groups = groupPermissions(permissions, (permission) => permission.name);
  const toggle = (name: string, checked: boolean) => {
    const next = new Set(selected);
    if (checked) next.add(name);
    else next.delete(name);
    onChange?.(next);
  };
  return (
    <div className="permission-groups">
      {groups.map((group) => (
        <fieldset key={group.prefix} className="permission-group">
          <legend>{group.prefix}</legend>
          {group.items.map((permission) => (
            <Checkbox
              key={permission.name}
              checked={selected.has(permission.name)}
              disabled={disabled ?? false}
              onChange={(event) => toggle(permission.name, event.target.checked)}
              label={
                <>
                  <code>{permission.name}</code> <RiskBadge risk={permission.risk} />
                </>
              }
              description={permission.description}
            />
          ))}
        </fieldset>
      ))}
      {groups.length === 0 ? <p className="empty">{t('roles.permissions.none')}</p> : null}
    </div>
  );
}
