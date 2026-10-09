import { useId, useState } from 'react';
import { useI18n } from '../../i18n/I18nProvider';
import { Button } from '../Button';
import {
  defaultCondition,
  defaultValue,
  emptyGroup,
  type Catalog,
  type Condition,
  type Field,
  type Group,
  type Node,
} from './filterModel';
import { useQueryLabels } from './queryLabels';
import { ValueEditor } from './ValueEditor';

type NodeProps<T extends Node> = {
  node: T;
  catalog: Catalog;
  onChange: (node: T) => void;
  onRemove?: () => void;
};

function ConditionRow({ node, catalog, onChange, onRemove }: NodeProps<Condition>) {
  const { t } = useI18n();
  const labels = useQueryLabels(catalog);
  const [search, setSearch] = useState('');
  const fieldId = useId();
  const field = catalog.fields.find((candidate) => candidate.key === node.field);
  const needle = search.trim().toLocaleLowerCase();
  const choices = catalog.fields.filter(
    (candidate) =>
      candidate.filterable &&
      candidate.operators.length > 0 &&
      (candidate.key === node.field ||
        needle === '' ||
        labels.fieldLabel(candidate.key).toLocaleLowerCase().includes(needle)),
  );
  const pick = (next: Field) => onChange(defaultCondition(next));
  return (
    <div className="query-condition">
      <label>
        <span>{t('query.findField')}</span>
        <input
          type="search"
          value={search}
          autoComplete="off"
          onChange={(event) => setSearch(event.target.value)}
          aria-controls={fieldId}
        />
      </label>
      <label>
        <span>{t('query.field')}</span>
        <select
          id={fieldId}
          value={node.field}
          onChange={(event) => {
            const next = catalog.fields.find((candidate) => candidate.key === event.target.value);
            if (next) pick(next);
          }}
        >
          {choices.map((candidate) => (
            <option key={candidate.key} value={candidate.key}>
              {labels.fieldLabel(candidate.key)}
            </option>
          ))}
        </select>
      </label>
      {field ? (
        <>
          <label>
            <span>{t('query.operator')}</span>
            <select
              value={node.op}
              onChange={(event) => {
                onChange({
                  type: 'condition',
                  field: node.field,
                  op: event.target.value,
                  ...defaultValue(field, event.target.value),
                });
              }}
            >
              {field.operators.map((op) => (
                <option key={op} value={op}>
                  {labels.opLabel(op)}
                </option>
              ))}
            </select>
          </label>
          <ValueEditor
            catalog={catalog}
            field={field}
            condition={node}
            onChange={(value) => onChange({ ...node, value })}
          />
        </>
      ) : (
        <p role="alert">{t('query.validation.field')}</p>
      )}
      {onRemove ? (
        <Button onClick={onRemove} aria-label={t('query.removeCondition')}>
          {t('query.remove')}
        </Button>
      ) : null}
    </div>
  );
}

export function GroupEditor({
  node,
  catalog,
  onChange,
  onRemove,
  depth = 1,
}: NodeProps<Group> & { depth?: number }) {
  const { t } = useI18n();
  const maxDepth = catalog.limits.maxDepth ?? 4;
  const first = catalog.fields.find((field) => field.filterable && field.operators.length > 0);
  const update = (index: number, next: Node) =>
    onChange({ ...node, children: node.children.map((child, i) => (i === index ? next : child)) });
  const remove = (index: number) =>
    onChange({ ...node, children: node.children.filter((_, i) => i !== index) });
  const canNest = depth < maxDepth;
  return (
    <fieldset className="query-group">
      <legend>{depth === 1 ? t('query.rootGroup') : t('query.group')}</legend>
      <label className="query-logic">
        <span>{t('query.match')}</span>
        <select
          value={node.logic}
          onChange={(event) =>
            onChange({ ...node, logic: event.target.value === 'or' ? 'or' : 'and' })
          }
        >
          <option value="and">{t('query.and')}</option>
          <option value="or">{t('query.or')}</option>
        </select>
      </label>
      {node.children.map((child, index) => (
        <div className="query-child" key={index}>
          {child.type === 'condition' ? (
            <ConditionRow
              node={child}
              catalog={catalog}
              onChange={(next) => update(index, next)}
              onRemove={() => remove(index)}
            />
          ) : (
            <GroupEditor
              node={child}
              catalog={catalog}
              depth={depth + 1}
              onChange={(next) => update(index, next)}
              onRemove={() => remove(index)}
            />
          )}
        </div>
      ))}
      <div className="query-actions">
        <Button
          disabled={!first}
          onClick={() =>
            first && onChange({ ...node, children: [...node.children, defaultCondition(first)] })
          }
        >
          {t('query.addCondition')}
        </Button>
        <Button
          disabled={!canNest}
          onClick={() => onChange({ ...node, children: [...node.children, emptyGroup()] })}
        >
          {t('query.addGroup')}
        </Button>
        {onRemove ? <Button onClick={onRemove}>{t('query.removeGroup')}</Button> : null}
      </div>
      {!canNest ? (
        <small className="field-hint">{t('query.depthLimit', { limit: maxDepth })}</small>
      ) : null}
    </fieldset>
  );
}
