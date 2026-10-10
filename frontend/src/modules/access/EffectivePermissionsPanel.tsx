import { useMemo, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useOptionalText } from '../../platform/i18n/optionalText';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Select, TextField } from '../../platform/ui/Field';
import { filterEffective, grantReasons } from './accessModel';
import { accessApi } from './api';
import { RiskBadge, useModuleName } from './PermissionPicker';
import { ExpiryCell } from './RoleAssignmentsScreen';
import type { PermissionRisk } from './types';

/** Why a person can do what they can: roles in force, the path that grants each permission, warnings. */
export function EffectivePermissionsPanel({ userId }: { userId: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const text = useOptionalText();
  const moduleName = useModuleName();
  const allowed = can('platform.roles.view') && can('organization.users.view_details');
  const effective = useAsync(
    (signal) =>
      allowed ? accessApi.effectivePermissions(userId, signal) : Promise.resolve(undefined),
    [userId, allowed],
  );
  const [search, setSearch] = useState('');
  const [risk, setRisk] = useState<PermissionRisk | ''>('');
  const [open, setOpen] = useState<string>('');
  const data = effective.data;
  const rows = useMemo(
    () => (data ? filterEffective(data, { search, risk }) : []),
    [data, search, risk],
  );

  if (!allowed) return <Alert kind="info">{t('effective.noPermission')}</Alert>;
  if (effective.loading && !data) return <p role="status">{t('state.loading')}</p>;
  if (effective.error) return <ApiErrorAlert error={effective.error} onRetry={effective.reload} />;
  if (!data) return null;

  return (
    <section className="adm-effective" aria-label={t('effective.title')}>
      <p className="subtitle">{t('effective.intro')}</p>
      {data.warnings.length > 0 ? (
        <Alert kind="warning">
          <p>
            <strong>{t('effective.sod.title')}</strong>
          </p>
          <ul>
            {data.warnings.map((rule) => (
              <li key={rule}>{text(`roles.sod.${rule}`, rule)}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {data.localAccount ? <Alert kind="info">{t('effective.localAccount')}</Alert> : null}
      {data.ceilings.length > 0 ? (
        <Alert kind="info">
          <p>{t('effective.ceilings')}</p>
          <ul>
            {data.ceilings.map((ceiling) => (
              <li key={ceiling}>{text(`effective.ceiling.${ceiling}`, ceiling)}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {data.excluded.length > 0 ? (
        <Alert kind="info">
          <p>{t('effective.excluded')}</p>
          <ul className="chips">
            {data.excluded.map((name) => (
              <li key={name}>
                <code>{name}</code>
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}

      <h3>{t('effective.roles')}</h3>
      {data.roles.length === 0 ? (
        <p className="empty">{t('effective.roles.empty')}</p>
      ) : (
        <ul className="adm-effective-roles">
          {data.roles.map((role) => (
            <li key={role.assignmentId}>
              {can('platform.roles.view') ? (
                <Link to={`/admin/roles/${encodeURIComponent(role.roleId)}`}>{role.roleName}</Link>
              ) : (
                role.roleName
              )}{' '}
              <Badge tone={role.source === 'direct' ? 'neutral' : 'info'}>
                {t(role.source === 'direct' ? 'effective.source.direct' : 'effective.source.group')}
              </Badge>{' '}
              {role.builtInAdmin ? (
                <Badge tone="danger">{t('effective.builtInAdmin')}</Badge>
              ) : null}{' '}
              <span className="adm-sub">
                {t('assignments.col.expires')}: <ExpiryCell expiresAt={role.expiresAt} />
              </span>
            </li>
          ))}
        </ul>
      )}

      <h3>{t('effective.permissions', { count: data.permissions.length })}</h3>
      <div className="adm-picker-bar">
        <TextField
          label={t('picker.search')}
          type="search"
          value={search}
          maxLength={100}
          onChange={(event) => setSearch(event.target.value)}
        />
        <Select
          label={t('picker.risk')}
          value={risk}
          onChange={(event) => setRisk(event.target.value as PermissionRisk | '')}
          options={[
            { value: '', label: t('picker.risk.all') },
            { value: 'normal', label: t('risk.normal') },
            { value: 'elevated', label: t('risk.elevated') },
            { value: 'high', label: t('risk.high') },
          ]}
        />
      </div>
      {rows.length === 0 ? (
        <p className="empty">{t('effective.permissions.empty')}</p>
      ) : (
        <table className="table adm-effective-table">
          <caption className="visually-hidden">{t('effective.title')}</caption>
          <thead>
            <tr>
              <th scope="col">{t('effective.col.permission')}</th>
              <th scope="col">{t('effective.col.module')}</th>
              <th scope="col">{t('effective.col.risk')}</th>
              <th scope="col">{t('effective.col.why')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((permission) => {
              const reasons = grantReasons(data, permission.name);
              const expanded = open === permission.name;
              return (
                <tr key={permission.name}>
                  <td>
                    <code>{permission.name}</code>
                  </td>
                  <td>{moduleName(permission.module)}</td>
                  <td>
                    <RiskBadge risk={permission.risk as PermissionRisk} />
                  </td>
                  <td>
                    <button
                      type="button"
                      className="btn-link"
                      aria-expanded={expanded}
                      aria-controls={`why-${permission.name}`}
                      onClick={() => setOpen(expanded ? '' : permission.name)}
                    >
                      {t('effective.why', { count: reasons.length })}
                    </button>
                    {expanded ? (
                      <ul id={`why-${permission.name}`} className="adm-why">
                        {reasons.map((reason, index) => (
                          <li key={`${reason.roleKey}:${index}`}>
                            {t(
                              reason.source === 'direct'
                                ? 'effective.why.direct'
                                : 'effective.why.group',
                              { role: reason.roleName },
                            )}
                          </li>
                        ))}
                        {reasons.length === 0 ? <li>{t('effective.why.unknown')}</li> : null}
                      </ul>
                    ) : null}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
