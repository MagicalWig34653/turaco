import { useI18n } from '../../platform/i18n/I18nProvider';
import { useOptionalText } from '../../platform/i18n/optionalText';
import { useModules } from '../../platform/modules/ModulesProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { riskCounts, templateDrift } from './accessModel';
import { useModuleName } from './PermissionPicker';
import type { Permission, RoleTemplate } from './types';

/** Gallery of the built-in Role Templates. A template never changes a Role after it was created. */
export function RoleTemplates({
  templates,
  permissions,
}: {
  templates: readonly RoleTemplate[];
  permissions: readonly Permission[];
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const { enabled } = useModules();
  const text = useOptionalText();
  const moduleName = useModuleName();
  const byName = new Map(permissions.map((permission) => [permission.name, permission]));
  const canManage = can('platform.roles.manage');
  if (templates.length === 0) return <p className="empty">{t('roles.templates.empty')}</p>;
  return (
    <ul className="adm-template-grid" aria-label={t('roles.templates.title')}>
      {templates.map((template) => {
        const counts = riskCounts(template.permissions, byName);
        const offModules = template.requiresModules.filter((module) => !enabled(module));
        const name = text(template.nameKey, template.name);
        return (
          <li key={template.key} className="adm-template-card">
            <h3>{name}</h3>
            <p className="adm-template-audience">
              {text(`roles.template.${template.key}.audience`, template.audience)}
            </p>
            <p>{text(template.descriptionKey, template.description)}</p>
            <p className="adm-template-facts">
              {t('roles.templates.permissions', { count: template.permissions.length })}
              {counts.elevated > 0
                ? ` · ${t('roles.templates.elevated', { count: counts.elevated })}`
                : ''}
              {counts.high > 0 ? ` · ${t('roles.templates.high', { count: counts.high })}` : ''}
            </p>
            <div className="adm-chip-row">
              {template.administratorAssignOnly ? (
                <Badge tone="danger">{t('roles.templates.administratorOnly')}</Badge>
              ) : null}
              {template.externalOnly ? (
                <Badge tone="warning">{t('roles.templates.externalOnly')}</Badge>
              ) : null}
              {offModules.map((module) => (
                <Badge key={module} tone="unknown">
                  {t('roles.templates.moduleOff', { module: moduleName(module) })}
                </Badge>
              ))}
            </div>
            {template.roles.length > 0 ? (
              <div className="adm-template-uses">
                <p>
                  <strong>{t('roles.templates.usedBy')}</strong>
                </p>
                <ul>
                  {template.roles.map((use) => {
                    const drift = templateDrift(use);
                    return (
                      <li key={use.roleId}>
                        <Link to={`/admin/roles/${encodeURIComponent(use.roleId)}`}>
                          {use.name}
                        </Link>{' '}
                        {drift.behind > 0 ? (
                          <Badge tone="warning">
                            {t('roles.templates.behind', { count: drift.behind })}
                          </Badge>
                        ) : (
                          <Badge tone="success">{t('roles.templates.current')}</Badge>
                        )}
                        {drift.extra > 0 ? (
                          <span className="adm-sub">
                            {' '}
                            {t('roles.templates.extra', { count: drift.extra })}
                          </span>
                        ) : null}
                      </li>
                    );
                  })}
                </ul>
              </div>
            ) : null}
            {canManage ? (
              <Link
                to={`/admin/roles/new?template=${encodeURIComponent(template.key)}`}
                className="btn btn-primary"
                aria-label={t('roles.templates.createFrom', { name })}
              >
                {t('roles.templates.create')}
              </Link>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
