//! When things run: the forms a schedule is written in, the next times it comes up, and whether
//! one has come due (routine-design §3.4, §3.5, §5.2). Pure: the clock and the local time zone
//! are passed in, and nothing here knows what runs. Bots' routines are the first user.

use jiff::Timestamp;
use jiff::Zoned;
use jiff::civil;
use jiff::tz::TimeZone;

/// How far ahead to look before calling a schedule one that never runs: long enough for the
/// next 29 February, or a leap day on a chosen weekday's date to pass.
const SEARCH_DAYS: usize = 366 * 8 + 2;

const TZ_PREFIX: &str = "CRON_TZ=";

/// A schedule that cannot be used, in words a model can act on.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub(crate) enum ScheduleError {
    #[error("schedule \"{text}\" cannot be read: {why}")]
    Unreadable { text: String, why: String },
    #[error("Unknown time zone \"{0}\": use an IANA name such as Asia/Shanghai.")]
    UnknownZone(String),
    #[error("That time has already passed: it is now {now}.")]
    Past { now: String },
    #[error("This schedule never runs.")]
    Never,
}

/// When something runs.
#[derive(Clone, Debug)]
pub(crate) enum Schedule {
    Cron(Cron),
    Once(Timestamp),
}

/// A five-field cron expression, in `zone` or, without one, in the local time zone.
#[derive(Clone, Debug)]
pub(crate) struct Cron {
    text: String,
    zone: Option<TimeZone>,
    minutes: u64,
    hours: u32,
    days: u32,
    months: u16,
    weekdays: u8,
    /// A field written as `*` matches everything; when both the day and the weekday are
    /// restricted, either may match (the rule of cron).
    days_any: bool,
    weekdays_any: bool,
}

impl Schedule {
    /// Reads a schedule as written. `in 20m` counts from `now`; every one-off becomes a moment,
    /// so [`Schedule::stored`] reads back the same.
    pub(crate) fn parse(text: &str, now: Timestamp, local: &TimeZone) -> Result<Schedule, ScheduleError> {
        let unreadable = |why: String| ScheduleError::Unreadable {
            text: text.trim().to_string(),
            why,
        };
        let (zone, rest) = match text.trim().strip_prefix(TZ_PREFIX) {
            Some(after) => {
                let (name, rest) = after.split_once(char::is_whitespace).ok_or_else(|| unreadable("nothing after the time zone".into()))?;
                let zone = TimeZone::get(name).map_err(|_| ScheduleError::UnknownZone(name.to_string()))?;
                (Some(zone), rest.trim())
            }
            None => (None, text.trim()),
        };
        if rest.is_empty() {
            return Err(unreadable("it is empty".into()));
        }
        if let Some(amount) = rest.strip_prefix("in ") {
            let span = duration(amount.trim()).map_err(unreadable)?;
            return now
                .checked_add(span)
                .map(Schedule::Once)
                .map_err(|e| unreadable(e.to_string()));
        }
        // What this file wrote itself: a moment with its offset.
        if let Ok(at) = rest.parse::<Timestamp>() {
            return Ok(Schedule::Once(at));
        }
        if let Some(at) = local_moment(rest) {
            let zone = zone.as_ref().unwrap_or(local);
            let at = zone
                .to_ambiguous_zoned(at)
                .compatible()
                .map_err(|e| unreadable(e.to_string()))?;
            return Ok(Schedule::Once(at.timestamp()));
        }
        // One space between fields, however it was written: models break the line in it too.
        let written = text.split_whitespace().collect::<Vec<_>>().join(" ");
        Cron::parse(&written, zone, rest).map(Schedule::Cron).map_err(unreadable)
    }

    /// Reads a schedule for a routine being saved: it must still come up.
    pub(crate) fn parse_new(text: &str, now: Timestamp, local: &TimeZone) -> Result<Schedule, ScheduleError> {
        let schedule = Schedule::parse(text, now, local)?;
        if schedule.next_after(now, local).is_some() {
            return Ok(schedule);
        }
        Err(match schedule {
            Schedule::Once(_) => ScheduleError::Past { now: moment_text(now, local) },
            Schedule::Cron(_) => ScheduleError::Never,
        })
    }

    /// What to keep: a cron expression as written, a one-off as its moment.
    pub(crate) fn stored(&self, local: &TimeZone) -> String {
        match self {
            Schedule::Cron(cron) => cron.text.clone(),
            Schedule::Once(at) => at.to_zoned(local.clone()).strftime("%Y-%m-%dT%H:%M:%S%:z").to_string(),
        }
    }

    pub(crate) fn is_once(&self) -> bool {
        matches!(self, Schedule::Once(_))
    }

    /// The first time strictly after `after`.
    pub(crate) fn next_after(&self, after: Timestamp, local: &TimeZone) -> Option<Timestamp> {
        match self {
            Schedule::Once(at) => (*at > after).then_some(*at),
            Schedule::Cron(cron) => cron.next_after(after, local),
        }
    }

    /// The next `count` times after `after`.
    pub(crate) fn upcoming(&self, after: Timestamp, count: usize, local: &TimeZone) -> Vec<Timestamp> {
        let mut out = Vec::with_capacity(count);
        let mut from = after;
        while out.len() < count {
            let Some(next) = self.next_after(from, local) else {
                break;
            };
            out.push(next);
            from = next;
        }
        out
    }

    /// What has come due since `anchor`, if anything: the latest time at or before `now`.
    /// Earlier ones that were missed are not made up (routine-design §5.3).
    pub(crate) fn due(&self, anchor: Timestamp, now: Timestamp, local: &TimeZone) -> Option<Timestamp> {
        let first = self.next_after(anchor, local)?;
        if first > now {
            return None;
        }
        Some(match self {
            Schedule::Once(at) => *at,
            Schedule::Cron(cron) => cron.latest_at_or_before(now, local).filter(|latest| *latest >= first).unwrap_or(first),
        })
    }
}

impl Cron {
    fn parse(text: &str, zone: Option<TimeZone>, fields: &str) -> Result<Cron, String> {
        let parts: Vec<&str> = fields.split_whitespace().collect();
        let [minute, hour, day, month, weekday] = parts[..] else {
            return Err(format!("a cron expression has 5 fields, this has {}", parts.len()));
        };
        let weekdays = field(weekday, "weekday", 0, 7)?;
        // 0 and 7 are both Sunday.
        let weekdays = (weekdays & 0x7f) as u8 | u8::from(weekdays & (1 << 7) != 0);
        Ok(Cron {
            text: text.to_string(),
            zone,
            minutes: field(minute, "minute", 0, 59)?,
            hours: field(hour, "hour", 0, 23)? as u32,
            days: field(day, "day", 1, 31)? as u32,
            months: field(month, "month", 1, 12)? as u16,
            weekdays,
            days_any: day == "*",
            weekdays_any: weekday == "*",
        })
    }

    fn date_matches(&self, date: civil::Date) -> bool {
        if self.months & (1 << date.month()) == 0 {
            return false;
        }
        let day = self.days & (1 << date.day()) != 0;
        let weekday = self.weekdays & (1 << date.weekday().to_sunday_zero_offset()) != 0;
        match (self.days_any, self.weekdays_any) {
            (false, false) => day || weekday,
            _ => day && weekday,
        }
    }

    /// The first matching minute on `date` at or after `from`.
    fn minute_at_or_after(&self, from: (u8, u8)) -> Option<(u8, u8)> {
        for hour in from.0..24 {
            if self.hours & (1 << hour) == 0 {
                continue;
            }
            let start = if hour == from.0 { from.1 } else { 0 };
            for minute in start..60 {
                if self.minutes & (1 << minute) != 0 {
                    return Some((hour, minute));
                }
            }
        }
        None
    }

    /// The last matching minute on `date` at or before `from`.
    fn minute_at_or_before(&self, from: (u8, u8)) -> Option<(u8, u8)> {
        for hour in (0..=from.0).rev() {
            if self.hours & (1 << hour) == 0 {
                continue;
            }
            let end = if hour == from.0 { from.1 } else { 59 };
            for minute in (0..=end).rev() {
                if self.minutes & (1 << minute) != 0 {
                    return Some((hour, minute));
                }
            }
        }
        None
    }

    fn next_after(&self, after: Timestamp, local: &TimeZone) -> Option<Timestamp> {
        let zone = self.zone.as_ref().unwrap_or(local);
        let here = after.to_zoned(zone.clone()).datetime();
        let mut date = here.date();
        // The minute after this one: a time on this very minute has passed, seconds or not.
        let mut from = Some((here.hour() as u8, here.minute() as u8 + 1)).map(|(h, m)| if m == 60 { (h + 1, 0) } else { (h, m) });
        for _ in 0..SEARCH_DAYS {
            if self.date_matches(date) {
                let mut at = from.filter(|(h, _)| *h < 24).unwrap_or((24, 0));
                while at.0 < 24 {
                    let Some(found) = self.minute_at_or_after(at) else {
                        break;
                    };
                    // A repeated hour gives the first pass, which may already be past; a
                    // skipped time gives the same minute after the jump, which a later minute
                    // of the day can land before.
                    let moment = moment_of(zone, date, found)?;
                    if moment > after {
                        return Some(moment);
                    }
                    at = if found.1 == 59 { (found.0 + 1, 0) } else { (found.0, found.1 + 1) };
                }
            }
            date = date.tomorrow().ok()?;
            from = Some((0, 0));
        }
        None
    }

    fn latest_at_or_before(&self, now: Timestamp, local: &TimeZone) -> Option<Timestamp> {
        let zone = self.zone.as_ref().unwrap_or(local);
        let here = now.to_zoned(zone.clone()).datetime();
        let mut date = here.date();
        let mut from = (here.hour() as u8, here.minute() as u8);
        for _ in 0..SEARCH_DAYS {
            if self.date_matches(date) {
                let mut at = Some(from);
                while let Some(limit) = at {
                    let Some(found) = self.minute_at_or_before(limit) else {
                        break;
                    };
                    let moment = moment_of(zone, date, found)?;
                    if moment <= now {
                        return Some(moment);
                    }
                    at = match found {
                        (0, 0) => None,
                        (hour, 0) => Some((hour - 1, 59)),
                        (hour, minute) => Some((hour, minute - 1)),
                    };
                }
            }
            date = date.yesterday().ok()?;
            from = (23, 59);
        }
        None
    }
}

/// The moment a local time means: a skipped time counts as the same minute after the jump, a
/// repeated one as its first pass.
fn moment_of(zone: &TimeZone, date: civil::Date, (hour, minute): (u8, u8)) -> Option<Timestamp> {
    let at = date.at(hour as i8, minute as i8, 0, 0);
    zone.to_ambiguous_zoned(at).compatible().ok().map(|z| z.timestamp())
}

/// One cron field as a bit set, with its bits numbered by value.
fn field(text: &str, name: &str, low: u8, high: u8) -> Result<u64, String> {
    let bad = |why: String| format!("the {name} field \"{text}\" {why}");
    let mut bits = 0u64;
    for part in text.split(',') {
        let (range, step) = match part.split_once('/') {
            Some((range, step)) => {
                let step: u8 = step.parse().ok().filter(|s| *s > 0).ok_or_else(|| bad(format!("has a step that is not a positive number: \"{step}\"")))?;
                (range, step)
            }
            None => (part, 1),
        };
        let number = |s: &str| -> Result<u8, String> {
            let value: u8 = s.parse().map_err(|_| {
                if s.chars().any(|c| c.is_ascii_alphabetic()) {
                    bad("uses a name or letter: write numbers".to_string())
                } else {
                    bad(format!("has something that is not a number: \"{s}\""))
                }
            })?;
            if value < low || value > high {
                return Err(bad(format!("has {value}, outside {low}-{high}")));
            }
            Ok(value)
        };
        let (from, to) = match range {
            "*" => (low, high),
            _ => match range.split_once('-') {
                Some((a, b)) => {
                    let (a, b) = (number(a)?, number(b)?);
                    if a > b {
                        return Err(bad(format!("has a range that runs backwards: {a}-{b}")));
                    }
                    (a, b)
                }
                // `5/15` means from 5 to the end, each 15.
                None if step > 1 => (number(range)?, high),
                None => {
                    let value = number(range)?;
                    (value, value)
                }
            },
        };
        let mut value = from;
        loop {
            bits |= 1 << value;
            match value.checked_add(step) {
                Some(next) if next <= to => value = next,
                _ => break,
            }
        }
    }
    Ok(bits)
}

/// `30s`, `20m`, `2h`, `1h30m`, `3d`.
fn duration(text: &str) -> Result<jiff::SignedDuration, String> {
    let bad = || format!("\"in {text}\" is not a length of time such as 20m, 2h, 1h30m or 3d");
    let mut seconds: i64 = 0;
    let mut digits = String::new();
    for c in text.chars().filter(|c| !c.is_whitespace()) {
        if c.is_ascii_digit() {
            digits.push(c);
            continue;
        }
        let unit: i64 = match c {
            's' => 1,
            'm' => 60,
            'h' => 3600,
            'd' => 86_400,
            _ => return Err(bad()),
        };
        let amount: i64 = digits.parse().map_err(|_| bad())?;
        seconds = amount.checked_mul(unit).and_then(|s| seconds.checked_add(s)).ok_or_else(bad)?;
        digits.clear();
    }
    if !digits.is_empty() || seconds <= 0 {
        return Err(bad());
    }
    Ok(jiff::SignedDuration::from_secs(seconds))
}

/// `2026-11-10 09:00`, with or without seconds.
fn local_moment(text: &str) -> Option<civil::DateTime> {
    let looks_like_a_date = text.len() >= 10 && text.as_bytes()[4] == b'-' && text.as_bytes()[7] == b'-';
    if !looks_like_a_date {
        return None;
    }
    text.parse::<civil::DateTime>().ok()
}

/// `Thu 2026-11-05 22:14`, for a model.
pub(crate) fn moment_text(at: Timestamp, local: &TimeZone) -> String {
    at.to_zoned(local.clone()).strftime("%a %Y-%m-%d %H:%M").to_string()
}

/// Several times in the local time zone, the date only when it changes:
/// `Thu Nov 5 00:00, 01:00, 02:00`.
pub(crate) fn runs_text(times: &[Timestamp], local: &TimeZone) -> String {
    let mut out = String::new();
    let mut last: Option<civil::Date> = None;
    for at in times {
        let zoned: Zoned = at.to_zoned(local.clone());
        if !out.is_empty() {
            out.push_str(", ");
        }
        if last == Some(zoned.date()) {
            out.push_str(&zoned.strftime("%H:%M").to_string());
        } else {
            out.push_str(&zoned.strftime("%a %b %-d %H:%M").to_string());
            last = Some(zoned.date());
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn shanghai() -> TimeZone {
        TimeZone::get("Asia/Shanghai").unwrap()
    }

    fn at(text: &str) -> Timestamp {
        text.parse().unwrap()
    }

    fn cron(text: &str) -> Schedule {
        Schedule::parse(text, at("2026-11-05T00:00:00Z"), &shanghai()).unwrap()
    }

    #[test]
    fn a_schedule_is_kept_on_one_line() {
        let written = cron("CRON_TZ=America/New_York\n*/20  9-16 * * 1-5 ");
        assert_eq!(written.stored(&shanghai()), "CRON_TZ=America/New_York */20 9-16 * * 1-5");
    }

    fn next(schedule: &Schedule, after: &str) -> String {
        schedule.next_after(at(after), &shanghai()).unwrap().to_string()
    }

    #[test]
    fn a_daily_time_is_in_the_local_zone() {
        // 2026-11-05 is a Thursday; 08:00 in Shanghai is midnight UTC.
        assert_eq!(next(&cron("0 8 * * *"), "2026-11-05T00:00:00Z"), "2026-11-06T00:00:00Z");
        assert_eq!(next(&cron("0 8 * * *"), "2026-11-04T23:59:59Z"), "2026-11-05T00:00:00Z");
    }

    #[test]
    fn a_time_passed_within_its_minute_is_passed() {
        assert_eq!(next(&cron("* * * * *"), "2026-11-05T00:00:30Z"), "2026-11-05T00:01:00Z");
        assert_eq!(next(&cron("* * * * *"), "2026-11-05T00:59:30Z"), "2026-11-05T01:00:00Z");
        assert_eq!(next(&cron("* * * * *"), "2026-11-05T15:59:30Z"), "2026-11-05T16:00:00Z");
    }

    #[test]
    fn lists_ranges_and_steps() {
        let schedule = cron("*/15 9-10 * * 1-5");
        let times = schedule.upcoming(at("2026-11-05T01:00:00Z"), 5, &shanghai());
        let shown: Vec<String> = times.iter().map(|t| t.to_zoned(shanghai()).strftime("%a %H:%M").to_string()).collect();
        assert_eq!(shown, ["Thu 09:15", "Thu 09:30", "Thu 09:45", "Thu 10:00", "Thu 10:15"]);
        let schedule = cron("5/20 8 * * *");
        let times = schedule.upcoming(at("2026-11-05T00:00:00Z"), 3, &shanghai());
        let shown: Vec<String> = times.iter().map(|t| t.to_zoned(shanghai()).strftime("%H:%M").to_string()).collect();
        assert_eq!(shown, ["08:05", "08:25", "08:45"]);
        let schedule = cron("0 8,17 * * *");
        assert_eq!(next(&schedule, "2026-11-05T00:00:00Z"), "2026-11-05T09:00:00Z");
    }

    #[test]
    fn sunday_is_both_zero_and_seven() {
        for text in ["0 9 * * 0", "0 9 * * 7"] {
            let moment = cron(text).next_after(at("2026-11-05T00:00:00Z"), &shanghai()).unwrap();
            assert_eq!(moment.to_zoned(shanghai()).strftime("%a %F").to_string(), "Sun 2026-11-08");
        }
    }

    #[test]
    fn a_day_and_a_weekday_together_mean_either() {
        // The 15th, or a Monday.
        let schedule = cron("0 9 15 * 1");
        let times = schedule.upcoming(at("2026-11-05T00:00:00Z"), 3, &shanghai());
        let shown: Vec<String> = times.iter().map(|t| t.to_zoned(shanghai()).strftime("%m-%d %a").to_string()).collect();
        assert_eq!(shown, ["11-09 Mon", "11-15 Sun", "11-16 Mon"]);
        // A star on either side leaves only the other.
        let shown = cron("0 9 15 * *").next_after(at("2026-11-05T00:00:00Z"), &shanghai()).unwrap();
        assert_eq!(shown.to_zoned(shanghai()).strftime("%m-%d").to_string(), "11-15");
    }

    #[test]
    fn a_leap_day_is_found_and_an_impossible_date_never_runs() {
        let moment = cron("0 9 29 2 *").next_after(at("2026-11-05T00:00:00Z"), &shanghai()).unwrap();
        assert_eq!(moment.to_zoned(shanghai()).strftime("%F").to_string(), "2028-02-29");
        let now = at("2026-11-05T00:00:00Z");
        assert_eq!(Schedule::parse_new("0 9 30 2 *", now, &shanghai()).unwrap_err(), ScheduleError::Never);
    }

    #[test]
    fn a_pinned_date_is_every_year() {
        let schedule = cron("0 9 10 11 *");
        assert!(!schedule.is_once());
        let times = schedule.upcoming(at("2026-11-05T00:00:00Z"), 2, &shanghai());
        let shown: Vec<String> = times.iter().map(|t| t.to_zoned(shanghai()).strftime("%F").to_string()).collect();
        assert_eq!(shown, ["2026-11-10", "2027-11-10"]);
    }

    #[test]
    fn a_zone_prefix_follows_that_zone_through_its_clock_changes() {
        // New York's clocks go back on 1 November 2026 and forward on 14 March 2027: the same
        // 10:00 there is 22:00 in Shanghai before and 23:00 after.
        let schedule = cron("CRON_TZ=America/New_York 0 10 * * 1-5");
        let before = schedule.next_after(at("2026-10-29T15:00:00Z"), &shanghai()).unwrap();
        assert_eq!(before.to_zoned(shanghai()).strftime("%m-%d %H:%M").to_string(), "10-30 22:00");
        let after = schedule.next_after(at("2026-11-02T00:00:00Z"), &shanghai()).unwrap();
        assert_eq!(after.to_zoned(shanghai()).strftime("%m-%d %H:%M").to_string(), "11-02 23:00");
    }

    #[test]
    fn a_skipped_time_runs_once_after_the_jump() {
        // New York jumps from 02:00 to 03:00 on 14 March 2027.
        let ny = TimeZone::get("America/New_York").unwrap();
        let schedule = Schedule::parse("30 2 * * *", at("2027-03-01T00:00:00Z"), &ny).unwrap();
        let from = at("2027-03-13T12:00:00Z");
        let times = schedule.upcoming(from, 3, &ny);
        let shown: Vec<String> = times.iter().map(|t| t.to_zoned(ny.clone()).strftime("%m-%d %H:%M").to_string()).collect();
        assert_eq!(shown, ["03-14 03:30", "03-15 02:30", "03-16 02:30"]);
    }

    #[test]
    fn minutes_through_a_jump_stay_in_order() {
        let ny = TimeZone::get("America/New_York").unwrap();
        let schedule = Schedule::parse("*/30 * * * *", at("2027-03-01T00:00:00Z"), &ny).unwrap();
        let from: Timestamp = "2027-03-14T06:15:00Z".parse().unwrap(); // 01:15 there
        let times = schedule.upcoming(from, 4, &ny);
        for pair in times.windows(2) {
            assert!(pair[0] < pair[1]);
        }
        let shown: Vec<String> = times.iter().map(|t| t.to_zoned(ny.clone()).strftime("%H:%M").to_string()).collect();
        assert_eq!(shown, ["01:30", "03:00", "03:30", "04:00"]);
    }

    #[test]
    fn a_repeated_hour_runs_once() {
        // New York repeats 01:00-02:00 on 1 November 2026.
        let ny = TimeZone::get("America/New_York").unwrap();
        let schedule = Schedule::parse("30 1 * * *", at("2026-10-01T00:00:00Z"), &ny).unwrap();
        let first = schedule.next_after(at("2026-11-01T04:00:00Z"), &ny).unwrap();
        assert_eq!(first.to_string(), "2026-11-01T05:30:00Z");
        let second = schedule.next_after(first, &ny).unwrap();
        assert_eq!(second.to_zoned(ny.clone()).strftime("%m-%d %H:%M").to_string(), "11-02 01:30");
    }

    #[test]
    fn no_zone_prefix_follows_the_local_zone() {
        let schedule = cron("0 8 * * *");
        let tokyo = TimeZone::get("Asia/Tokyo").unwrap();
        assert_eq!(schedule.next_after(at("2026-11-05T00:00:00Z"), &tokyo).unwrap().to_string(), "2026-11-05T23:00:00Z");
    }

    #[test]
    fn one_offs_become_moments() {
        let now = at("2026-11-05T14:14:00Z"); // 22:14 in Shanghai
        let local = shanghai();
        let soon = Schedule::parse("in 1h30m", now, &local).unwrap();
        assert!(soon.is_once());
        assert_eq!(soon.next_after(now, &local).unwrap().to_string(), "2026-11-05T15:44:00Z");
        assert_eq!(soon.stored(&local), "2026-11-05T23:44:00+08:00");
        // Reads back as itself.
        let again = Schedule::parse(&soon.stored(&local), at("2030-01-01T00:00:00Z"), &local).unwrap();
        assert_eq!(again.next_after(now, &local), soon.next_after(now, &local));

        let dated = Schedule::parse("2026-11-10 09:00", now, &local).unwrap();
        assert_eq!(dated.next_after(now, &local).unwrap().to_string(), "2026-11-10T01:00:00Z");
        let there = Schedule::parse("CRON_TZ=America/New_York 2026-11-10 09:30", now, &local).unwrap();
        assert_eq!(there.next_after(now, &local).unwrap().to_string(), "2026-11-10T14:30:00Z");
        assert!(Schedule::parse("in 3d", now, &local).is_ok());
        // Once it has passed nothing comes up.
        assert_eq!(dated.next_after(at("2026-11-11T00:00:00Z"), &local), None);
    }

    #[test]
    fn a_time_gone_by_says_what_time_it_is() {
        let now = at("2026-11-05T14:14:00Z");
        let err = Schedule::parse_new("2026-11-05 21:00", now, &shanghai()).unwrap_err();
        assert_eq!(err.to_string(), "That time has already passed: it is now Thu 2026-11-05 22:14.");
    }

    #[test]
    fn unreadable_schedules_say_why() {
        let now = at("2026-11-05T14:14:00Z");
        let local = shanghai();
        let text = |s: &str| Schedule::parse(s, now, &local).unwrap_err().to_string();
        assert!(text("every day").contains("5 fields"));
        assert!(text("0 9 * * MON").contains("write numbers"));
        assert!(text("60 9 * * *").contains("outside 0-59"));
        assert!(text("0 24 * * *").contains("outside 0-23"));
        assert!(text("0 9 0 * *").contains("outside 1-31"));
        assert!(text("0 9 * 13 *").contains("outside 1-12"));
        assert!(text("0 9 * * 8").contains("outside 0-7"));
        assert!(text("*/0 9 * * *").contains("positive"));
        assert!(text("5-1 9 * * *").contains("backwards"));
        assert!(text("in soon").contains("length of time"));
        assert!(text("in 5").contains("length of time"));
        assert!(text("in 0m").contains("length of time"));
        assert!(text("").contains("empty"));
        assert_eq!(text("CRON_TZ=Mars/Base 0 9 * * *"), "Unknown time zone \"Mars/Base\": use an IANA name such as Asia/Shanghai.");
    }

    #[test]
    fn what_is_due_is_the_latest_missed_time() {
        let local = shanghai();
        let schedule = cron("0 * * * *");
        let anchor = at("2026-11-05T00:30:00Z");
        // Not yet.
        assert_eq!(schedule.due(anchor, at("2026-11-05T00:59:59Z"), &local), None);
        // On time.
        assert_eq!(schedule.due(anchor, at("2026-11-05T01:00:05Z"), &local), Some(at("2026-11-05T01:00:00Z")));
        // Slept through several: only the last counts.
        assert_eq!(schedule.due(anchor, at("2026-11-05T07:20:00Z"), &local), Some(at("2026-11-05T07:00:00Z")));
        // Handled already.
        assert_eq!(schedule.due(at("2026-11-05T07:00:00Z"), at("2026-11-05T07:20:00Z"), &local), None);
    }

    #[test]
    fn what_is_due_copes_with_a_long_absence() {
        let local = shanghai();
        let schedule = cron("* * * * *");
        let due = schedule.due(at("2026-01-01T00:00:00Z"), at("2026-11-05T07:20:30Z"), &local);
        assert_eq!(due, Some(at("2026-11-05T07:20:00Z")));
        let yearly = cron("0 9 10 11 *");
        let due = yearly.due(at("2026-11-01T00:00:00Z"), at("2028-12-01T00:00:00Z"), &local);
        assert_eq!(due.unwrap().to_zoned(local).strftime("%F").to_string(), "2028-11-10");
    }

    #[test]
    fn a_one_off_is_due_however_late() {
        let local = shanghai();
        let schedule = Schedule::parse("2026-11-10 09:00", at("2026-11-05T00:00:00Z"), &local).unwrap();
        let moment = at("2026-11-10T01:00:00Z");
        assert_eq!(schedule.due(at("2026-11-05T00:00:00Z"), at("2026-11-09T00:00:00Z"), &local), None);
        assert_eq!(schedule.due(at("2026-11-05T00:00:00Z"), at("2027-03-01T00:00:00Z"), &local), Some(moment));
        assert_eq!(schedule.due(moment, at("2027-03-01T00:00:00Z"), &local), None);
    }

    #[test]
    fn times_are_written_for_people() {
        let local = shanghai();
        let times = [at("2026-11-04T16:00:00Z"), at("2026-11-04T17:00:00Z"), at("2026-11-05T16:00:00Z")];
        assert_eq!(runs_text(&times, &local), "Thu Nov 5 00:00, 01:00, Fri Nov 6 00:00");
        assert_eq!(moment_text(times[0], &local), "Thu 2026-11-05 00:00");
    }
}
