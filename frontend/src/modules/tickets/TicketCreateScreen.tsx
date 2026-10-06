import { useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextArea, TextField } from '../../platform/ui/Field';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { assetsApi } from '../assets/api';
import { IncidentBanner } from '../incidents/IncidentBanner';
import { Suggestions } from '../knowledge/Suggestions';
import { ticketsApi } from './api';
import type { Ticket } from './types';

function ReportIcon({ kind }: { kind: 'device' | 'message' | 'book' | 'check' }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {kind === 'device' ? (
        <>
          <rect x="3" y="4" width="18" height="13" rx="2" />
          <path d="M8 21h8M12 17v4" />
        </>
      ) : kind === 'book' ? (
        <>
          <path d="M12 5v16M3 4h5a4 4 0 0 1 4 2 4 4 0 0 1 4-2h5v15h-5a4 4 0 0 0-4 2 4 4 0 0 0-4-2H3z" />
        </>
      ) : kind === 'check' ? (
        <path d="m5 12 4 4L19 6" />
      ) : (
        <path d="M5 4h14a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H9l-6 3V6a2 2 0 0 1 2-2Z" />
      )}
    </svg>
  );
}

/** Raising a ticket asks for little: what is wrong, and optionally which of my devices. */
export function TicketCreateScreen() {
  const { t } = useI18n();
  const devices = useAsync(async (signal) => assetsApi.mine(undefined, signal), []);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [assetId, setAssetId] = useState('');
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const [created, setCreated] = useState<Ticket>();
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (submitting.current || !title.trim()) return;
    submitting.current = true;
    setBusy(true);
    setError(undefined);
    try {
      setCreated(
        await ticketsApi.create({
          title: title.trim(),
          description: description.trim(),
          ...(assetId ? { assetId } : {}),
        }),
      );
    } catch (cause) {
      setError(asApiError(cause));
      submitting.current = false;
      setBusy(false);
    }
  };

  const names = devices.data?.productNames ?? {};
  if (created)
    return (
      <div className="report-workspace report-success">
        <Card className="report-success-card">
          <span className="report-success-icon">
            <ReportIcon kind="check" />
          </span>
          <p className="report-eyebrow">{t('reportPolish.sent')}</p>
          <h1
            tabIndex={-1}
            ref={(node) => {
              node?.focus();
            }}
          >
            {t('reportPolish.successTitle')}
          </h1>
          <p>{t('reportPolish.successIntro')}</p>
          <div className="report-receipt">
            <span className="report-reference">{created.reference}</span>
            <strong>{created.title}</strong>
          </div>
          <Link className="btn btn-primary" to={`/support/${encodeURIComponent(created.id)}`}>
            {t('reportPolish.openTicket')} <span aria-hidden="true">→</span>
          </Link>
        </Card>
        <Card className="report-next">
          <h2>{t('reportPolish.next')}</h2>
          <ol>
            <li>{t('reportPolish.nextReview')}</li>
            <li>{t('reportPolish.nextFollow')}</li>
          </ol>
          <Link to="/support">
            {t('tickets.back')} <span aria-hidden="true">→</span>
          </Link>
        </Card>
      </div>
    );

  return (
    <div className="report-workspace">
      <header className="report-heading">
        <div>
          <p className="report-eyebrow">{t('reportPolish.eyebrow')}</p>
          <h1>{t('reportPolish.title')}</h1>
          <p className="subtitle">{t('reportPolish.intro')}</p>
        </div>
        <Link to="/support" className="back-link">
          <span aria-hidden="true">←</span> {t('tickets.back')}
        </Link>
      </header>
      <IncidentBanner />
      <div className="report-layout">
        <form className="report-form" onSubmit={(event) => void submit(event)}>
          {error ? <ApiErrorAlert error={error} /> : null}
          <Card className="report-section">
            <div className="report-section-heading">
              <span className="report-step" aria-hidden="true">
                01
              </span>
              <div>
                <h2>{t('reportPolish.describe')}</h2>
                <p>{t('reportPolish.describeHint')}</p>
              </div>
            </div>
            <TextField
              label={t('tickets.field.title')}
              hint={t('reportPolish.titleHint')}
              placeholder={t('reportPolish.titlePlaceholder')}
              value={title}
              maxLength={200}
              required
              autoFocus
              disabled={busy}
              onChange={(event) => setTitle(event.target.value)}
            />
            <TextArea
              label={t('tickets.field.description')}
              hint={`${t('tickets.field.description.hint')} · ${t('reportPolish.characters', { count: description.length, max: 5000 })}`}
              placeholder={t('reportPolish.descriptionPlaceholder')}
              value={description}
              rows={8}
              maxLength={5000}
              disabled={busy}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Card>
          <Card className="report-section">
            <div className="report-section-heading">
              <span className="report-step" aria-hidden="true">
                02
              </span>
              <div>
                <h2>{t('reportPolish.equipment')}</h2>
                <p>{t('tickets.field.device.hint')}</p>
              </div>
              <span className="report-optional">{t('reportPolish.optional')}</span>
            </div>
            {devices.loading ? <Skeleton lines={2} /> : null}
            {devices.error ? (
              <p className="report-device-note" role="status">
                {t('reportPolish.devicesUnavailable')}
              </p>
            ) : null}
            <fieldset className="report-devices" disabled={busy}>
              <legend className="report-sr-only">{t('tickets.field.device')}</legend>
              <label className={`report-device ${assetId === '' ? 'is-selected' : ''}`}>
                <input
                  type="radio"
                  name="report-device"
                  value=""
                  checked={assetId === ''}
                  onChange={() => setAssetId('')}
                />
                <span className="report-device-icon">
                  <ReportIcon kind="message" />
                </span>
                <span>
                  <strong>{t('tickets.field.device.none')}</strong>
                  <small>{t('reportPolish.noDeviceHint')}</small>
                </span>
              </label>
              {devices.data?.items.map((a) => (
                <label
                  key={a.id}
                  className={`report-device ${assetId === a.id ? 'is-selected' : ''}`}
                >
                  <input
                    type="radio"
                    name="report-device"
                    value={a.id}
                    checked={assetId === a.id}
                    onChange={() => setAssetId(a.id)}
                  />
                  <span className="report-device-icon">
                    <ReportIcon kind="device" />
                  </span>
                  <span>
                    <strong>{names[a.productId] ?? a.reference}</strong>
                    <small className="report-reference">{a.reference}</small>
                  </span>
                </label>
              ))}
            </fieldset>
          </Card>
          <div className="report-submit">
            <p>{t('reportPolish.submitHint')}</p>
            <Button type="submit" variant="primary" busy={busy} disabled={!title.trim()}>
              {t(busy ? 'reportPolish.sending' : 'reportPolish.submit')}{' '}
              <span aria-hidden="true">→</span>
            </Button>
          </div>
        </form>
        <aside className="report-help" aria-label={t('reportPolish.help')}>
          <Card className="report-help-card">
            <span className="report-help-icon">
              <ReportIcon kind="book" />
            </span>
            <p className="report-eyebrow">{t('reportPolish.help')}</p>
            <h2>{t('reportPolish.helpTitle')}</h2>
            <p>{t('reportPolish.helpIntro')}</p>
            <div className="report-suggestions">
              <Suggestions text={title} />
            </div>
            <Link className="report-card-footer" to="/knowledge">
              {t('reportPolish.browse')} <span aria-hidden="true">→</span>
            </Link>
          </Card>
          <Card className="report-tips">
            <h2>{t('reportPolish.tips')}</h2>
            <ul>
              <li>{t('reportPolish.tipWhen')}</li>
              <li>{t('reportPolish.tipTried')}</li>
              <li>{t('reportPolish.tipPrivacy')}</li>
            </ul>
          </Card>
        </aside>
      </div>
    </div>
  );
}
