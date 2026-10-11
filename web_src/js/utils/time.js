import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc.js';
import {getCurrentLocale} from '../utils.js';

dayjs.extend(utc);

/**
 * Returns an array of millisecond-timestamps of start-of-week days (Sundays)
 *
 * @param startConfig The start date. Can take any type that `Date` accepts.
 * @param endConfig The end date. Can take any type that `Date` accepts.
 */
export function startDaysBetween(startDate, endDate) {
  const start = dayjs.utc(startDate);
  const end = dayjs.utc(endDate);

  let current = start;

  // Ensure the start date is a Sunday
  while (current.day() !== 0) {
    current = current.add(1, 'day');
  }

  const startDays = [];
  while (current.isBefore(end)) {
    startDays.push(current.valueOf());
    current = current.add(1, 'week');
  }

  return startDays;
}

export function firstStartDateAfterDate(inputDate) {
  if (!(inputDate instanceof Date)) {
    throw new Error('Invalid date');
  }
  const dayOfWeek = inputDate.getUTCDay();
  const daysUntilSunday = 7 - dayOfWeek;
  const resultDate = new Date(inputDate);
  resultDate.setUTCDate(resultDate.getUTCDate() + daysUntilSunday);
  return resultDate.valueOf();
}

export function fillEmptyStartDaysWithZeroes(startDays, data) {
  const result = {};

  for (const startDay of startDays) {
    result[startDay] = data[startDay] || {'week': startDay, 'additions': 0, 'deletions': 0, 'commits': 0};
  }

  return Object.values(result);
}

let dateFormat;

// format a Date object to document's locale, but with 24h format from user's current locale because this
// option is a personal preference of the user, not something that the document's locale should dictate.
export function formatDatetime(date) {
  if (!dateFormat) {
    // TODO: replace `hour12` with `Intl.Locale.prototype.getHourCycles` once there is broad browser support
    dateFormat = new Intl.DateTimeFormat(getCurrentLocale(), {
      day: 'numeric',
      month: 'short',
      year: 'numeric',
      hour: 'numeric',
      hour12: !Number.isInteger(Number(new Intl.DateTimeFormat([], {hour: 'numeric'}).format())),
      minute: '2-digit',
      timeZoneName: 'short',
    });
  }
  return dateFormat.format(date);
}

// offset of the zone from UTC at the instant ts (ms), in ms
function zoneOffset(ts, timeZone) {
  const p = Object.fromEntries(new Intl.DateTimeFormat('en-US', {
    timeZone, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric', second: 'numeric',
  }).formatToParts(new Date(ts)).map((x) => [x.type, Number(x.value)]));
  return Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second) - Math.floor(ts / 1000) * 1000;
}

/**
 * Returns the ISO (UTC) string of a datetime-local value ("2026-10-25T02:30") read as wall-clock time in timeZone.
 * An ambiguous time (clocks turned back) takes the earlier instant; a time that does not exist (clocks turned forward)
 * is read with the offset from before the change. Without a valid timeZone the browser's zone is used.
 */
export function zonedLocalToISO(local, timeZone) {
  const m = /^(\d+)-(\d+)-(\d+)T(\d+):(\d+)/.exec(local);
  if (!m) return '';
  let valid = Boolean(timeZone);
  try {
    if (valid) new Intl.DateTimeFormat('en-US', {timeZone});
  } catch {
    valid = false;
  }
  if (!valid) return new Date(local).toISOString();
  const wall = Date.UTC(m[1], m[2] - 1, m[3], m[4], m[5]);
  const day = 24 * 3600 * 1000;
  const candidates = Array.from(new Set([zoneOffset(wall - day, timeZone), zoneOffset(wall + day, timeZone)]), (o) => wall - o);
  const exact = candidates.filter((c) => c + zoneOffset(c, timeZone) === wall);
  return new Date(exact.length ? Math.min(...exact) : candidates[0]).toISOString();
}

// "Europe/Paris (UTC+02:00)" for the zone at the given instant (ms)
export function zoneLabel(timeZone, ts = Date.now()) {
  const o = Math.round(zoneOffset(ts, timeZone) / 60000);
  const sign = o < 0 ? '-' : '+';
  const abs = Math.abs(o);
  return `${timeZone} (UTC${sign}${String(Math.floor(abs / 60)).padStart(2, '0')}:${String(abs % 60).padStart(2, '0')})`;
}

// The datetime-local value ("2026-10-25T02:30") of the instant (ISO string or ms) as wall-clock time in timeZone
export function isoToZonedLocal(iso, timeZone) {
  const ts = new Date(iso).getTime();
  let offset = 0;
  try {
    offset = zoneOffset(ts, timeZone);
  } catch { // no valid zone: the browser's
    offset = -new Date(ts).getTimezoneOffset() * 60000;
  }
  return new Date(ts + offset).toISOString().slice(0, 16);
}
