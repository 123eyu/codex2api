import assert from 'node:assert/strict'
import test from 'node:test'
import {
  aiCredits,
  isQuotaStale,
  lowestModelRemaining,
  modelQuotaEntries,
  normalizeFraction,
  parseModelList,
  parseRefreshTokenLines,
  quotaWindowKey,
  shortQuotaGroupLabel,
  summarizeQuotaGroups,
} from './antigravityQuota.ts'

const snapshot = {
  models: [
    { model_id: 'claude-opus-5-5-high', remaining_fraction: 0.9992 },
    { model_id: 'gemini-3-flash', remaining_fraction: 1 },
    { model_id: 'gpt-oss-120b-medium', remaining_fraction: 0.4 },
  ],
  quota_groups: [
    {
      display_name: 'Gemini Models',
      description: 'Models within this group: Gemini Flash, Gemini Pro',
      buckets: [
        { bucket_id: 'gemini-weekly', window: 'weekly', remaining_fraction: 0.98907405, reset_time: '2026-10-10T09:10:46Z' },
        { bucket_id: 'gemini-5h', window: '5h', remaining_fraction: 1, reset_time: '2026-10-08T13:24:39Z' },
      ],
    },
    {
      display_name: 'Claude and GPT models',
      buckets: [
        { bucket_id: '3p-weekly', window: 'weekly', remaining_fraction: 0.25 },
        { bucket_id: '3p-5h', window: '5h', remaining_fraction: 0.6 },
      ],
    },
  ],
  ai_credits: { credits: 0 },
  updated_at: '2026-10-08T13:24:42Z',
}

test('quota groups summarise 5h before weekly with the tightest bucket as lowest', () => {
  const groups = summarizeQuotaGroups(snapshot)
  assert.equal(groups.length, 2)
  assert.deepEqual(groups.map((group) => group.label), ['Gemini', 'Claude / GPT'])
  assert.deepEqual(groups[0].windows.map((window) => window.window), ['5h', 'weekly'])
  assert.equal(groups[0].windows[1].resetAt, '2026-10-10T09:10:46Z')
  assert.equal(groups[1].lowest, 0.25)
  assert.match(groups[0].description, /Gemini Flash/)
})

test('legacy groups field and camelCase buckets are still read', () => {
  const groups = summarizeQuotaGroups({
    models: [],
    groups: [{ displayName: 'Weekly', buckets: [{ bucketId: 'x', window: '7d', remainingFraction: 50 }] }],
  })
  assert.equal(groups.length, 1)
  assert.equal(groups[0].windows[0].window, 'weekly')
  assert.equal(groups[0].windows[0].fraction, 0.5)
})

test('group labels are shortened and empty names fall back to an index', () => {
  assert.equal(shortQuotaGroupLabel('Gemini Models', 0), 'Gemini')
  assert.equal(shortQuotaGroupLabel('Claude and GPT models', 1), 'Claude / GPT')
  assert.equal(shortQuotaGroupLabel('  ', 2), '#3')
})

test('window keys recognise common spellings', () => {
  assert.equal(quotaWindowKey('5h'), '5h')
  assert.equal(quotaWindowKey('WEEKLY'), 'weekly')
  assert.equal(quotaWindowKey('daily'), 'other')
})

test('model entries accept array and object shapes and report the lowest remaining fraction', () => {
  assert.deepEqual(modelQuotaEntries(snapshot).map(([model]) => model), [
    'claude-opus-5-5-high',
    'gemini-3-flash',
    'gpt-oss-120b-medium',
  ])
  assert.equal(lowestModelRemaining(snapshot), 0.4)
  assert.deepEqual(
    modelQuotaEntries({ models: { 'gemini-2.5-pro': { remaining_fraction: 0.75 } } }).map(([model]) => model),
    ['gemini-2.5-pro'],
  )
  assert.equal(lowestModelRemaining(undefined), null)
})

test('fractions accept percentages and clamp out-of-range values', () => {
  assert.equal(normalizeFraction(75), 0.75)
  assert.equal(normalizeFraction(-1), 0)
  assert.equal(normalizeFraction('1'), null)
})

test('ai credits are only reported when the snapshot carries a numeric balance', () => {
  assert.deepEqual(aiCredits(snapshot), { credits: 0, expiryDate: '' })
  assert.equal(aiCredits({ models: [] }), null)
})

test('quota snapshots older than six hours are stale', () => {
  const now = Date.parse('2026-10-08T20:00:00Z')
  assert.equal(isQuotaStale('2026-10-08T13:24:42Z', now), true)
  assert.equal(isQuotaStale('2026-10-08T18:00:00Z', now), false)
  assert.equal(isQuotaStale('', now), false)
})

test('model and refresh token inputs are split, trimmed and de-duplicated', () => {
  assert.deepEqual(parseModelList('a, b\n\na\nc'), ['a', 'b', 'c'])
  assert.deepEqual(parseRefreshTokenLines('1//rt-a\r\n\n# note\n1//rt-b\n1//rt-a'), ['1//rt-a', '1//rt-b'])
})
