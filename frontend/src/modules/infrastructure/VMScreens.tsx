import { impactUrl } from '../services/helpers';
import { useState, type FormEvent } from 'react';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { infrastructureApi as api } from './api';
import type { VM, VMState } from './types';
const states: VMState[] = ['running', 'stopped', 'unknown', 'decommissioned'];
const activeStates: VMState[] = ['running', 'stopped', 'unknown'];
const reasons = ['retired', 'migrated', 'deleted', 'other'] as const;
function VMForm({
  vm,
  onClose,
  onDone,
}: {
  vm?: VM;
  onClose: () => void;
  onDone: (id: string) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(vm?.name ?? '');
  const [state, setState] = useState<VMState>('unknown');
  const [vcpu, setVcpu] = useState(vm?.vcpu ?? 1);
  const [memoryMb, setMemoryMb] = useState(vm?.memoryMb ?? 1024);
  const [managementAddress, setManagementAddress] = useState(vm?.managementAddress ?? '');
  const [networkNote, setNetworkNote] = useState(vm?.networkNote ?? '');
  const [notes, setNotes] = useState(vm?.notes ?? '');
  const [hypervisorAssetId, setHypervisorAssetId] = useState(vm?.hypervisorAssetId ?? '');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const result = vm
        ? await api.updateVM(vm.id, {
            name,
            vcpu,
            memoryMb,
            managementAddress,
            networkNote,
            notes,
            expectedVersion: vm.version,
          })
        : await api.createVM({
            name,
            state,
            vcpu,
            memoryMb,
            managementAddress,
            networkNote,
            notes,
            hypervisorAssetId: hypervisorAssetId || null,
          });
      onDone(result.id);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog title={t(vm ? 'infra.vm.edit' : 'infra.vm.create')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error && <ApiErrorAlert error={error} />}
        <label>
          {t('infra.name')}
          <input required value={name} onChange={(event) => setName(event.target.value)} />
        </label>
        {!vm && (
          <>
            <label>
              {t('infra.vm.state')}
              <select value={state} onChange={(event) => setState(event.target.value as VMState)}>
                {activeStates.map((value) => (
                  <option key={value} value={value}>
                    {t(`infra.vm.state.${value}`)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('infra.vm.hypervisor')}
              <input
                value={hypervisorAssetId}
                onChange={(event) => setHypervisorAssetId(event.target.value)}
              />
            </label>
          </>
        )}
        <label>
          {t('infra.vm.vcpu')}
          <input
            type="number"
            min="1"
            required
            value={vcpu}
            onChange={(event) => setVcpu(Number(event.target.value))}
          />
        </label>
        <label>
          {t('infra.vm.memory')}
          <input
            type="number"
            min="1"
            required
            value={memoryMb}
            onChange={(event) => setMemoryMb(Number(event.target.value))}
          />
        </label>
        <label>
          {t('infra.vm.address')}
          <input
            value={managementAddress}
            onChange={(event) => setManagementAddress(event.target.value)}
          />
        </label>
        <label>
          {t('infra.vm.networkNote')}
          <input value={networkNote} onChange={(event) => setNetworkNote(event.target.value)} />
        </label>
        <label>
          {t('infra.vm.notes')}
          <textarea value={notes} onChange={(event) => setNotes(event.target.value)} />
        </label>
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
export function VMListScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [q, setQ] = useState('');
  const [state, setState] = useState('');
  const [hypervisor, setHypervisor] = useState('');
  const [filters, setFilters] = useState({ q: '', state: '', hypervisorAssetId: '' });
  const [create, setCreate] = useState(false);
  const list = usePagedList((cursor, signal) => api.vms(filters, cursor, signal), [filters]);
  return (
    <>
      <PageHeader
        title={t('infra.vms')}
        actions={
          can('infrastructure.manage') ? (
            <Button onClick={() => setCreate(true)}>{t('infra.vm.create')}</Button>
          ) : undefined
        }
      />
      <form
        className="form"
        onSubmit={(event) => {
          event.preventDefault();
          setFilters({ q, state, hypervisorAssetId: hypervisor });
        }}
      >
        <label>
          {t('infra.search')}
          <input value={q} onChange={(event) => setQ(event.target.value)} />
        </label>
        <label>
          {t('infra.vm.state')}
          <select value={state} onChange={(event) => setState(event.target.value)}>
            <option value="">{t('filters.all')}</option>
            {states.map((value) => (
              <option key={value} value={value}>
                {t(`infra.vm.state.${value}`)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('infra.vm.hypervisor')}
          <input value={hypervisor} onChange={(event) => setHypervisor(event.target.value)} />
        </label>
        <Button type="submit">{t('filters.apply')}</Button>
      </form>
      {list.error && <ApiErrorAlert error={list.error} onRetry={list.reload} />}
      <ul>
        {list.items.map((vm) => (
          <li key={vm.id}>
            <Link to={`/infrastructure/virtual-machines/${vm.id}`}>{vm.name}</Link> ·{' '}
            {t(`infra.vm.state.${vm.state}`)} ·{' '}
            {vm.hypervisorAssetReference ?? vm.hypervisorAssetId ?? '–'}
          </li>
        ))}
      </ul>
      {list.hasMore && <Button onClick={list.loadMore}>{t('action.loadMore')}</Button>}
      {create && (
        <VMForm
          onClose={() => setCreate(false)}
          onDone={(id) => {
            setCreate(false);
            navigate(`/infrastructure/virtual-machines/${id}`);
          }}
        />
      )}
    </>
  );
}
export function VMDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => api.vm(id, signal), [id]);
  const [dialog, setDialog] = useState<'edit' | 'state' | 'hypervisor' | 'decommission'>();
  const [state, setState] = useState<VMState>('unknown');
  const [hypervisor, setHypervisor] = useState('');
  const [reason, setReason] = useState<string>('retired');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const vm = loaded.data;
  const run = async () => {
    if (!vm) return;
    setBusy(true);
    setError(undefined);
    try {
      if (dialog === 'state') await api.stateVM(id, state, vm.version);
      if (dialog === 'hypervisor') await api.hypervisorVM(id, hypervisor || null, vm.version);
      if (dialog === 'decommission') await api.decommissionVM(id, reason, vm.version);
      setDialog(undefined);
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader title={vm?.name ?? t('infra.vm.detail')} />
      {loaded.error && <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />}
      {vm && (
        <>
          <p>
            <Link to="/infrastructure/virtual-machines">{t('infra.back')}</Link>
          </p>
          {(can('services.view') || can('services.manage')) && (
            <p>
              <Link to={impactUrl('vm', vm.id)}>{t('services.impact')}</Link>
            </p>
          )}
          <dl className="facts">
            <dt>{t('infra.vm.state')}</dt>
            <dd>{t(`infra.vm.state.${vm.state}`)}</dd>
            <dt>{t('infra.vm.vcpu')}</dt>
            <dd>{vm.vcpu}</dd>
            <dt>{t('infra.vm.memory')}</dt>
            <dd>{vm.memoryMb}</dd>
            <dt>{t('infra.vm.address')}</dt>
            <dd>{vm.managementAddress ?? '–'}</dd>
            <dt>{t('infra.vm.hypervisor')}</dt>
            <dd>{vm.hypervisorAssetReference ?? vm.hypervisorAssetId ?? '–'}</dd>
            <dt>{t('infra.vm.networkNote')}</dt>
            <dd>{vm.networkNote ?? '–'}</dd>
            <dt>{t('infra.vm.notes')}</dt>
            <dd>{vm.notes ?? '–'}</dd>
          </dl>
          {can('infrastructure.manage') && vm.state !== 'decommissioned' && (
            <>
              <Button onClick={() => setDialog('edit')}>{t('infra.vm.edit')}</Button>
              <Button
                onClick={() => {
                  setState(vm.state);
                  setDialog('state');
                }}
              >
                {t('infra.vm.changeState')}
              </Button>
              <Button
                onClick={() => {
                  setHypervisor(vm.hypervisorAssetId ?? '');
                  setDialog('hypervisor');
                }}
              >
                {t('infra.vm.changeHypervisor')}
              </Button>
              <Button onClick={() => setDialog('decommission')}>
                {t('infra.vm.decommission')}
              </Button>
            </>
          )}
          {dialog === 'edit' && (
            <VMForm
              vm={vm}
              onClose={() => setDialog(undefined)}
              onDone={() => {
                setDialog(undefined);
                loaded.reload();
              }}
            />
          )}
          {dialog && dialog !== 'edit' && (
            <Dialog title={t(`infra.vm.${dialog}`)} onClose={() => setDialog(undefined)}>
              {error && <ApiErrorAlert error={error} />}
              {dialog === 'state' && (
                <label>
                  {t('infra.vm.state')}
                  <select
                    value={state}
                    onChange={(event) => setState(event.target.value as VMState)}
                  >
                    {activeStates.map((value) => (
                      <option key={value} value={value}>
                        {t(`infra.vm.state.${value}`)}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              {dialog === 'hypervisor' && (
                <label>
                  {t('infra.vm.hypervisor')}
                  <input
                    value={hypervisor}
                    onChange={(event) => setHypervisor(event.target.value)}
                  />
                </label>
              )}
              {dialog === 'decommission' && (
                <label>
                  {t('infra.reason')}
                  <select value={reason} onChange={(event) => setReason(event.target.value)}>
                    {reasons.map((value) => (
                      <option key={value} value={value}>
                        {t(`infra.vm.reason.${value}`)}
                      </option>
                    ))}
                  </select>
                </label>
              )}
              <div className="dialog-actions">
                <Button onClick={() => setDialog(undefined)}>{t('action.cancel')}</Button>
                <Button variant="primary" busy={busy} onClick={() => void run()}>
                  {t('action.save')}
                </Button>
              </div>
            </Dialog>
          )}
        </>
      )}
    </>
  );
}
