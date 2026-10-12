import { useEffect, useMemo, useRef, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useLocation } from '../../platform/router/Router';
import { Button } from '../../platform/ui/Button';
import { DataTable } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { AiError } from './AiError';
import { aiApi } from './api';
import { watchConsentApproval } from './consentDelay';
import { authorizeParams, parseRedirectUris } from './model';
import { dataClasses, type McpClient, type McpClientFields } from './types';

/** Administrator allowlist of MCP OAuth clients (F12 A-D). Public clients only; there is no secret to show. */
export function McpClients({ editable }: { editable: boolean }) {
  const { t } = useI18n();
  const clients = useAsync(aiApi.mcpClients, []);
  const [editing, setEditing] = useState<McpClient | 'new'>();
  return (
    <section>
      <h2>{t('ai.mcp.clients')}</h2>
      <p>{t('ai.mcp.clientsIntro')}</p>
      {clients.data && !clients.data.mounted && <p role="status">{t('ai.mcp.notMounted')}</p>}
      {clients.data?.resource && <p>{t('ai.mcp.endpoint', { url: clients.data.resource })}</p>}
      <AiError error={clients.error} />
      <Button onClick={clients.reload}>{t('ai.reload')}</Button>
      {editable && <Button onClick={() => setEditing('new')}>{t('ai.mcp.createClient')}</Button>}
      <DataTable
        caption={t('ai.mcp.clients')}
        emptyText={t('ai.none')}
        rows={clients.data?.items ?? []}
        rowKey={(c) => c.id}
        loading={clients.loading}
        columns={[
          { key: 'name', header: t('ai.field.displayName'), render: (c) => c.displayName },
          {
            key: 'clientId',
            header: t('ai.mcp.clientId'),
            render: (c) => <code>{c.clientId}</code>,
          },
          {
            key: 'enabled',
            header: t('ai.field.enabled'),
            render: (c) => t(c.enabled ? 'ai.yes' : 'ai.no'),
          },
          { key: 'grants', header: t('ai.mcp.activeGrants'), render: (c) => c.activeGrants },
          {
            key: 'actions',
            header: t('ai.actions'),
            render: (c) => (
              <Button onClick={() => setEditing(c)}>{t(editable ? 'ai.edit' : 'ai.view')}</Button>
            ),
          },
        ]}
      />
      {editing && (
        <McpClientEditor
          original={editing === 'new' ? undefined : editing}
          editable={editable}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            clients.reload();
          }}
        />
      )}
    </section>
  );
}

const emptyClient: McpClientFields = {
  displayName: '',
  redirectUris: [],
  allowedDataClasses: ['public_reference'],
  enabled: true,
};

function McpClientEditor({
  original,
  editable,
  onClose,
  onSaved,
}: {
  original?: McpClient | undefined;
  editable: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<McpClientFields>(original ?? emptyClient);
  const [uris, setUris] = useState((original?.redirectUris ?? []).join('\n'));
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  return (
    <Dialog
      title={t(original ? 'ai.mcp.editClient' : 'ai.mcp.createClient')}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (lock.current || !editable) return;
          lock.current = true;
          setBusy(true);
          setError(undefined);
          void aiApi
            .saveMcpClient({ ...draft, redirectUris: parseRedirectUris(uris) }, original)
            .then(onSaved, setError)
            .finally(() => {
              lock.current = false;
              setBusy(false);
            });
        }}
      >
        <AiError error={error} />
        {original && (
          <p>
            {t('ai.mcp.clientId')}: <code>{original.clientId}</code>
          </p>
        )}
        <fieldset disabled={!editable || busy}>
          <label>
            {t('ai.field.displayName')}
            <input
              type="text"
              maxLength={100}
              required
              value={draft.displayName}
              onChange={(e) => setDraft({ ...draft, displayName: e.target.value })}
            />
          </label>
          <label>
            {t('ai.mcp.redirectUris')}
            <textarea
              required
              rows={3}
              value={uris}
              aria-describedby="mcp-redirect-hint"
              onChange={(e) => setUris(e.target.value)}
            />
          </label>
          <p id="mcp-redirect-hint">{t('ai.mcp.redirectHint')}</p>
          <label>
            <span>{t('ai.field.enabled')}</span>
            <input
              type="checkbox"
              checked={draft.enabled}
              onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })}
            />
          </label>
          <fieldset>
            <legend>{t('ai.allowedClasses')}</legend>
            {dataClasses.map((c) => (
              <label key={c}>
                <span>{t(`ai.class.${c}`)}</span>
                <input
                  type="checkbox"
                  checked={draft.allowedDataClasses.includes(c)}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      allowedDataClasses: e.target.checked
                        ? [...draft.allowedDataClasses, c]
                        : draft.allowedDataClasses.filter((value) => value !== c),
                    })
                  }
                />
              </label>
            ))}
          </fieldset>
          {editable && (
            <Button type="submit" busy={busy}>
              {t('ai.save')}
            </Button>
          )}
        </fieldset>
        <Button disabled={busy} onClick={onClose}>
          {t('action.close')}
        </Button>
      </form>
    </Dialog>
  );
}

/**
 * The OAuth consent page of the MCP server (`/oauth/authorize`). It runs inside the signed-in shell, shows what the
 * application would get and leaves only through the redirect URL the server returns; an unregistered client or
 * redirect URI shows an error and never redirects.
 */
export function ConsentScreen() {
  const { t } = useI18n();
  const { search } = useLocation();
  const params = useMemo(() => authorizeParams(search), [search]);
  const preview = useAsync((signal) => aiApi.previewAuthorize(params, signal), [params]);
  const [error, setError] = useState<unknown>();
  const [leaving, setLeaving] = useState(false);
  const [approvalReady, setApprovalReady] = useState(false);
  const lock = useRef(false);
  useEffect(() => watchConsentApproval(setApprovalReady), []);
  function decide(decision: 'approve' | 'deny') {
    if (lock.current || (decision === 'approve' && !approvalReady)) return;
    lock.current = true;
    setError(undefined);
    void aiApi.authorize(params, decision).then(
      ({ redirectUrl }) => {
        setLeaving(true);
        window.location.assign(redirectUrl);
      },
      (e: unknown) => {
        lock.current = false;
        setError(e);
      },
    );
  }
  const p = preview.data;
  // Defence in depth against clickjacking besides the frame-ancestors header: no decision inside a frame.
  const framed = window.top !== window.self;
  return (
    <div className="ai-consent">
      <PageHeader title={t('ai.mcp.consentTitle')} />
      <AiError error={preview.error ?? error} />
      {preview.loading && <p role="status">{t('state.loading')}</p>}
      {leaving && <p role="status">{t('ai.mcp.redirecting')}</p>}
      {framed && <p role="alert">{t('ai.mcp.framed')}</p>}
      {p && !leaving && !framed && (
        <section>
          <p>{t('ai.mcp.consentIntro', { client: p.clientName })}</p>
          <p>{t('ai.mcp.consentRedirect', { uri: p.redirectUri })}</p>
          <h2>{t('ai.mcp.consentTools')}</h2>
          {p.tools.length === 0 ? (
            <p>{t('ai.mcp.noTools')}</p>
          ) : (
            <ul>
              {p.tools.map((tool) => (
                <li key={tool}>
                  <code>{tool}</code>
                </li>
              ))}
            </ul>
          )}
          <h2>{t('ai.mcp.consentClasses')}</h2>
          <ul>
            {p.dataClasses.map((c) => (
              <li key={c}>{t(`ai.class.${c}`)}</li>
            ))}
          </ul>
          <p>{t('ai.mcp.consentWarning')}</p>
          <p>{t('ai.mcp.consentDuration', { hours: p.grantHours })}</p>
          <Button disabled={!approvalReady} onClick={() => decide('approve')}>
            {t('ai.mcp.approve')}
          </Button>
          <Button onClick={() => decide('deny')}>{t('ai.mcp.deny')}</Button>
        </section>
      )}
    </div>
  );
}

/** The signed-in User's own MCP grants, each of which can be ended at once. */
export function ConnectionsScreen() {
  const { t, locale } = useI18n();
  const grants = useAsync(aiApi.mcpGrants, []);
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  async function revoke(id: string) {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await aiApi.revokeMcpGrant(id);
      grants.reload();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="ai-connections">
      <PageHeader title={t('ai.mcp.connections')} />
      <AiError error={grants.error ?? error} />
      <DataTable
        caption={t('ai.mcp.connections')}
        emptyText={t('ai.mcp.connectionsEmpty')}
        rows={grants.data?.items ?? []}
        rowKey={(g) => g.id}
        loading={grants.loading}
        columns={[
          { key: 'client', header: t('ai.field.displayName'), render: (g) => g.clientName },
          {
            key: 'created',
            header: t('ai.mcp.connectedAt'),
            render: (g) => formatDateTime(locale, g.createdAt),
          },
          {
            key: 'expires',
            header: t('ai.mcp.expiresAt'),
            render: (g) => formatDateTime(locale, g.expiresAt),
          },
          {
            key: 'used',
            header: t('ai.mcp.lastUsedAt'),
            render: (g) =>
              g.lastUsedAt ? formatDateTime(locale, g.lastUsedAt) : t('ai.mcp.never'),
          },
          {
            key: 'actions',
            header: t('ai.actions'),
            render: (g) => (
              <Button disabled={busy} onClick={() => void revoke(g.id)}>
                {t('ai.mcp.revoke')}
              </Button>
            ),
          },
        ]}
      />
    </div>
  );
}
