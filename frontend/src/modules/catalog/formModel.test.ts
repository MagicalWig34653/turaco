import { describe, expect, it } from 'vitest';
import { en } from '../../platform/i18n/messages.en';
import { fieldErrorKey, initialValues, toAnswers } from './formModel';
import type { FormField } from './types';

const fields: FormField[] = [
  { key: 'reason', type: 'longtext', label: 'Reason', required: true, maxLength: 20 },
  { key: 'title', type: 'text', label: 'Title', required: false, maxLength: 5 },
  { key: 'count', type: 'number', label: 'Count', required: false, min: 1, max: 5 },
  { key: 'urgent', type: 'boolean', label: 'Urgent', required: false },
  { key: 'needBy', type: 'date', label: 'Needed by', required: false },
  {
    key: 'color',
    type: 'select',
    label: 'Color',
    required: false,
    options: [{ value: 'black', label: 'Black' }],
  },
  {
    key: 'device',
    type: 'product',
    label: 'Device',
    required: true,
    productOptions: [{ id: 'p1', name: 'Latitude' }],
  },
];

describe('initialValues', () => {
  it('starts booleans as false and everything else empty', () => {
    expect(initialValues(fields)).toEqual({
      reason: '',
      title: '',
      count: '',
      urgent: false,
      needBy: '',
      color: '',
      device: '',
    });
  });
});

describe('toAnswers', () => {
  it('trims, converts numbers and omits empty optional answers', () => {
    const values = {
      ...initialValues(fields),
      reason: '  need a laptop ',
      count: ' 3 ',
      device: 'p1',
      urgent: true,
    };
    expect(toAnswers(fields, values)).toEqual({
      answers: { reason: 'need a laptop', count: 3, urgent: true, device: 'p1' },
      errors: {},
    });
  });

  it('always sends booleans, also unchecked ones, and never treats them as missing', () => {
    const required: FormField[] = [
      { key: 'confirm', type: 'boolean', label: 'Confirm', required: true },
    ];
    expect(toAnswers(required, { confirm: false })).toEqual({
      answers: { confirm: false },
      errors: {},
    });
  });

  it('reports required fields that are empty or blank', () => {
    const { errors } = toAnswers(fields, { ...initialValues(fields), reason: '   ' });
    expect(errors).toEqual({ reason: 'required', device: 'required' });
  });

  it('checks numbers, ranges and text length like the server does', () => {
    const base = { ...initialValues(fields), reason: 'x', device: 'p1' };
    expect(toAnswers(fields, { ...base, count: '2.5' }).errors).toEqual({ count: 'invalid_type' });
    expect(toAnswers(fields, { ...base, count: 'abc' }).errors).toEqual({ count: 'invalid_type' });
    expect(toAnswers(fields, { ...base, count: '0' }).errors).toEqual({ count: 'out_of_range' });
    expect(toAnswers(fields, { ...base, count: '6' }).errors).toEqual({ count: 'out_of_range' });
    expect(toAnswers(fields, { ...base, title: 'abcdef' }).errors).toEqual({ title: 'too_long' });
    expect(toAnswers(fields, { ...base, title: '🙂🙂🙂🙂🙂' }).errors).toEqual({});
  });
});

describe('fieldErrorKey', () => {
  it('has a message for every code the server can send and a generic fallback', () => {
    for (const code of [
      'required',
      'invalid_type',
      'too_long',
      'out_of_range',
      'invalid_choice',
      'unknown_field',
      'invalid_date',
      'invalid_characters',
      'not_allowed',
      'not_active',
      'not_found',
    ]) {
      expect(fieldErrorKey(code) in en, code).toBe(true);
    }
    expect(fieldErrorKey('something_new')).toBe('requests.fieldError.generic');
    expect('requests.fieldError.generic' in en).toBe(true);
  });
});
