import {firstStartDateAfterDate, startDaysBetween, fillEmptyStartDaysWithZeroes, zonedLocalToISO, isoToZonedLocal, zoneLabel} from './time.js';

test('startDaysBetween', () => {
  expect(startDaysBetween(new Date('2024-02-15'), new Date('2024-04-18'))).toEqual([
    1708214400000,
    1708819200000,
    1709424000000,
    1710028800000,
    1710633600000,
    1711238400000,
    1711843200000,
    1712448000000,
    1713052800000,
  ]);
});

test('firstStartDateAfterDate', () => {
  const expectedDate = new Date('2024-02-18').getTime();
  expect(firstStartDateAfterDate(new Date('2024-02-15'))).toEqual(expectedDate);

  expect(() => firstStartDateAfterDate('2024-02-15')).toThrowError('Invalid date');
});
test('fillEmptyStartDaysWithZeroes with data', () => {
  expect(fillEmptyStartDaysWithZeroes([1708214400000, 1708819200000, 1708819300000], {
    1708214400000: {'week': 1708214400000, 'additions': 1, 'deletions': 2, 'commits': 3},
    1708819200000: {'week': 1708819200000, 'additions': 4, 'deletions': 5, 'commits': 6},
  })).toEqual([
    {'week': 1708214400000, 'additions': 1, 'deletions': 2, 'commits': 3},
    {'week': 1708819200000, 'additions': 4, 'deletions': 5, 'commits': 6},
    {
      'additions': 0,
      'commits': 0,
      'deletions': 0,
      'week': 1708819300000,
    }]);
});

test('fillEmptyStartDaysWithZeroes with empty array', () => {
  expect(fillEmptyStartDaysWithZeroes([], {})).toEqual([]);
});

test('zonedLocalToISO reads the wall-clock time in the given zone', () => {
  expect(zonedLocalToISO('2026-07-01T12:00', 'Europe/Paris')).toBe('2026-07-01T10:00:00.000Z');
  expect(zonedLocalToISO('2026-01-01T12:00', 'Europe/Paris')).toBe('2026-01-01T11:00:00.000Z');
  expect(zonedLocalToISO('2026-07-01T12:00', 'America/Los_Angeles')).toBe('2026-07-01T19:00:00.000Z');
  expect(zonedLocalToISO('2026-07-01T12:00', 'UTC')).toBe('2026-07-01T12:00:00.000Z');
  expect(zonedLocalToISO('2026-07-01T12:00', 'Asia/Kolkata')).toBe('2026-07-01T06:30:00.000Z');
});

test('zonedLocalToISO handles clock changes', () => {
  // 2026-10-25 02:30 happens twice in Paris: the earlier one is still CEST (UTC+2)
  expect(zonedLocalToISO('2026-10-25T02:30', 'Europe/Paris')).toBe('2026-10-25T00:30:00.000Z');
  // 2026-03-29 02:30 does not exist in Paris: read with the offset from before the change (UTC+1)
  expect(zonedLocalToISO('2026-03-29T02:30', 'Europe/Paris')).toBe('2026-03-29T01:30:00.000Z');
});

test('zonedLocalToISO falls back to the browser zone', () => {
  const local = '2026-07-01T12:00';
  const expected = new Date(local).toISOString();
  expect(zonedLocalToISO(local, '')).toBe(expected);
  expect(zonedLocalToISO(local, 'Not/AZone')).toBe(expected);
  expect(zonedLocalToISO('', 'UTC')).toBe('');
});

test('zoneLabel', () => {
  expect(zoneLabel('Europe/Paris', Date.UTC(2026, 6, 1))).toBe('Europe/Paris (UTC+02:00)');
  expect(zoneLabel('Asia/Kolkata', Date.UTC(2026, 6, 1))).toBe('Asia/Kolkata (UTC+05:30)');
});

test('isoToZonedLocal is the inverse of zonedLocalToISO', () => {
  expect(isoToZonedLocal('2026-07-01T10:00:00.000Z', 'Europe/Paris')).toBe('2026-07-01T12:00');
  expect(isoToZonedLocal('2026-01-01T11:00:00.000Z', 'Europe/Paris')).toBe('2026-01-01T12:00');
  expect(isoToZonedLocal('2026-07-01T06:30:00.000Z', 'Asia/Kolkata')).toBe('2026-07-01T12:00');
  expect(isoToZonedLocal('2026-07-01T00:00:00.000Z', 'UTC')).toBe('2026-07-01T00:00');
  for (const zone of ['Europe/Paris', 'America/Los_Angeles', 'UTC']) {
    expect(zonedLocalToISO(isoToZonedLocal('2026-03-10T08:15:00.000Z', zone), zone)).toBe('2026-03-10T08:15:00.000Z');
  }
});

test('isoToZonedLocal and zonedLocalToISO across a DST transition', () => {
  // Europe/Paris, clocks go forward at 01:00Z on 2026-03-29 (02:00 -> 03:00)
  expect(isoToZonedLocal('2026-03-29T00:30:00.000Z', 'Europe/Paris')).toBe('2026-03-29T01:30');
  expect(isoToZonedLocal('2026-03-29T01:30:00.000Z', 'Europe/Paris')).toBe('2026-03-29T03:30');
  expect(zonedLocalToISO('2026-03-29T01:30', 'Europe/Paris')).toBe('2026-03-29T00:30:00.000Z');
  expect(zonedLocalToISO('2026-03-29T03:30', 'Europe/Paris')).toBe('2026-03-29T01:30:00.000Z');
  // America/Los_Angeles, clocks go forward at 10:00Z on 2026-03-08 (02:00 -> 03:00)
  expect(isoToZonedLocal('2026-03-08T09:30:00.000Z', 'America/Los_Angeles')).toBe('2026-03-08T01:30');
  expect(isoToZonedLocal('2026-03-08T10:30:00.000Z', 'America/Los_Angeles')).toBe('2026-03-08T03:30');
  expect(zonedLocalToISO('2026-03-08T01:30', 'America/Los_Angeles')).toBe('2026-03-08T09:30:00.000Z');
  expect(zonedLocalToISO('2026-03-08T03:30', 'America/Los_Angeles')).toBe('2026-03-08T10:30:00.000Z');
  // clocks go back in Paris at 01:00Z on 2026-10-25: the earlier of the two 02:30 is read
  expect(zonedLocalToISO('2026-10-25T02:30', 'Europe/Paris')).toBe('2026-10-25T00:30:00.000Z');
  expect(isoToZonedLocal('2026-10-25T00:30:00.000Z', 'Europe/Paris')).toBe('2026-10-25T02:30');
  expect(isoToZonedLocal('2026-10-25T01:30:00.000Z', 'Europe/Paris')).toBe('2026-10-25T02:30');
  // every unambiguous instant around the transitions round trips
  for (const [zone, start] of [['Europe/Paris', Date.UTC(2026, 2, 28, 12)], ['America/Los_Angeles', Date.UTC(2026, 2, 7, 12)]]) {
    for (let h = 0; h < 72; h++) {
      const iso = new Date(start + h * 3600 * 1000).toISOString();
      expect(zonedLocalToISO(isoToZonedLocal(iso, zone), zone)).toBe(iso);
    }
  }
});
