// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// maxRepeats is how many occurrences of a recurring event that have not
// ended are followed before the rest are dropped.
const maxRepeats = 1000

// maxPast is how many occurrences of a recurring event that have ended
// are passed over, on the way to the first that has not, before the
// event is left out: enough for an hourly event that began a century
// ago.
const maxPast = 1_000_000

// maxText is how many characters of an event's title or location, or a
// feed's name, are kept, so a feed cannot make a view too large to send.
const maxText = 200

// event is one occurrence of an event in a feed.
type event struct {
	// key identifies the occurrence across refreshes: its UID and start.
	key                    string
	title, location, notes string
	source                 string // the feed's name, or the source's
	start, end             time.Time
	allDay                 bool
}

// feed is a parsed iCalendar feed.
type feed struct {
	name string // X-WR-CALNAME, if the feed names itself
	cal  *ical.Calendar
}

// errNotICS reports a feed that cannot be read. The decoder's own
// errors quote the feed, which a panel showing them could leak.
var errNotICS = errors.New("not an iCalendar feed")

// parse reads an iCalendar feed. The decoder panics on some malformed
// lines, so a panic is an error too.
func parse(r io.Reader) (f feed, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = errNotICS
		}
	}()
	data, err := io.ReadAll(r)
	if err != nil {
		return feed{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return feed{}, errors.New("not an iCalendar feed: it is empty")
	}
	cal, err := ical.NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		// Only a feed the decoder refuses is mended, so a line it reads
		// as a property stays one.
		cal, err = ical.NewDecoder(bytes.NewReader(mend(data))).Decode()
	}
	if err != nil {
		// Including a feed that ends before its END:VCALENDAR.
		return feed{}, errNotICS
	}
	return feed{name: clip(text(cal.Props, "X-WR-CALNAME"), maxText), cal: cal}, nil
}

// property is how a content line starts: a name, then its parameters
// or its value.
var property = regexp.MustCompile(`^[A-Za-z0-9-]+[;:]`)

// mend makes a line that is not a property continue the one before it,
// as a feed that wraps long lines but drops the space that marks the
// continuation would have it, rather than fail the whole feed.
func mend(data []byte) []byte {
	var b bytes.Buffer
	for i, line := range bytes.Split(data, []byte("\n")) {
		if i > 0 {
			b.WriteByte('\n')
		}
		if text := bytes.TrimRight(line, "\r"); i > 0 && len(text) > 0 && text[0] != ' ' && text[0] != '\t' && !property.Match(text) {
			b.WriteByte(' ')
		}
		b.Write(line)
	}
	return b.Bytes()
}

// occurrences returns the occurrences of f's events that end after from
// and start before to, sorted by start. A time with no zone, and a
// date, is in loc, as is a time in a zone the database does not know.
// Events that cannot be read are left out, as are cancelled ones.
//
// When ctx ends, the recurring events not yet followed are left out too,
// and complete is false. A rule that can never match makes the
// recurrence library search to the year 9999 in one step, which nothing
// can interrupt, so the rules are followed in a goroutine of their own
// that may outlive the call.
func (f feed) occurrences(ctx context.Context, from, to time.Time, loc *time.Location) (events []event, complete bool) {
	z := zones{loc: loc, known: map[string]*time.Location{}}
	moved := map[string]bool{}
	var singles, series []*ical.Event
	for _, e := range f.cal.Events() {
		if status, _ := e.Status(); status == ical.EventCancelled && e.Props.Get(ical.PropRecurrenceID) == nil {
			continue
		}
		if id := e.Props.Get(ical.PropRecurrenceID); id != nil {
			if t, _, err := z.time(id); err == nil {
				moved[occurrenceKey(&e, t)] = true
			}
		}
		// A moved occurrence is one occurrence, even with a rule of its
		// own, which a change to it "and all following" gives it. An
		// EXDATE may exclude even the start of an event without a rule.
		if e.Props.Get(ical.PropRecurrenceID) == nil && (e.Props.Get(ical.PropRecurrenceRule) != nil || e.Props.Get(ical.PropRecurrenceDates) != nil || e.Props.Get(ical.PropExceptionDates) != nil) {
			series = append(series, &e)
		} else {
			singles = append(singles, &e)
		}
	}
	for _, e := range singles {
		if status, _ := e.Status(); status == ical.EventCancelled {
			continue
		}
		start, length, allDay, err := z.span(e)
		if err != nil {
			continue
		}
		ev := f.describe(e).at(start, length, allDay)
		// A moved occurrence keeps the key of the one it replaces, which
		// may differ from that of one now at the same time.
		if id := e.Props.Get(ical.PropRecurrenceID); id != nil {
			if t, _, err := z.time(id); err == nil {
				ev.key = occurrenceKey(e, t)
			}
		}
		events = appendIn(events, ev, from, to)
	}
	followed := make(chan []event, len(series))
	go func() {
		for _, e := range series {
			if ctx.Err() != nil {
				return
			}
			followed <- f.repeat(nil, e, &z, moved, from, to)
		}
	}()
	complete = true
	for range series {
		select {
		case evs := <-followed:
			events = append(events, evs...)
			continue
		case <-ctx.Done():
			complete = false
		}
		break
	}
	slices.SortFunc(events, func(a, b event) int {
		return cmp.Or(a.start.Compare(b.start), cmp.Compare(a.title, b.title), cmp.Compare(a.key, b.key))
	})
	return events, complete
}

// repeat appends the occurrences of the recurring event e between from
// and to that no other event in the feed moved or cancelled.
func (f feed) repeat(events []event, e *ical.Event, z *zones, moved map[string]bool, from, to time.Time) (out []event) {
	defer func() {
		// The recurrence library panics on some rules it cannot follow;
		// only this event is left out.
		if recover() != nil {
			out = events
		}
	}()
	start, length, allDay, err := z.span(e)
	if err != nil {
		return events
	}
	// The series repeats by the clock on the wall in its zone, which the
	// recurrence library follows in UTC, so that each time is read in
	// the zone once, by RFC 5545's rules for clock changes.
	// A start the clocks skip still sets the time of day the rule
	// repeats at.
	loc := start.Location()
	first, _, _, _ := z.clock(e.Props.Get(ical.PropDateTimeStart))
	var set rrule.Set
	r, until := rule(e, first, start, length, allDay, from, to)
	if r != nil {
		set.RRule(r)
	}
	set.DTStart(first)
	// The start is the first occurrence, whether or not the rule
	// matches it; the set returns a time only once.
	set.RDate(first)
	// RDATEs are instants, which a clock showing its time twice would
	// not tell apart, so they join the rule's times after those are
	// read in the zone.
	var rdates []time.Time
	periods := map[int64]time.Duration{}
	for _, s := range z.times(e.Props.Values(ical.PropRecurrenceDates)) {
		rdates = append(rdates, s.t)
		if s.period {
			periods[s.t.Unix()] = s.length
		}
	}
	// A date excluded from a series with start times excludes that day
	// in the series' zone.
	skipped := map[int]bool{}
	excluded := map[int64]bool{}
	for _, s := range z.times(e.Props.Values(ical.PropExceptionDates)) {
		switch {
		case allDay:
			// A day excluded by a time on it, as some feeds write.
			skipped[dayNumber(s.t.In(loc))] = true
		case s.date:
			skipped[dayNumber(s.t)] = true
		default:
			excluded[s.t.Unix()] = true
		}
	}
	seen := map[int64]bool{}
	base := f.describe(e)
	next := merge(set.Iterator(), loc, start, until, rdates)
	for past, ahead := 0, 0; past < maxPast && ahead < maxRepeats; {
		t, ok := next()
		if !ok {
			break
		}
		if !t.Before(to) {
			// A clock change moves a time by hours at most.
			if t.After(to.Add(24 * time.Hour)) {
				break
			}
			continue
		}
		// A time the clocks skip is read as one after the gap, which may
		// be another occurrence's.
		if seen[t.Unix()] || excluded[t.Unix()] {
			continue
		}
		seen[t.Unix()] = true
		l := length
		if d, ok := periods[t.Unix()]; ok {
			l = span{d: d}
		}
		if !l.end(t, allDay).After(from) {
			past++
			continue
		}
		ahead++
		if !moved[base.key+keyTime(t)] && !skipped[dayNumber(t.In(start.Location()))] {
			events = appendIn(events, base.at(t, l, allDay), from, to)
		}
	}
	return events
}

// merge returns the times of rule, clock times read in loc, up to until
// if it is not zero, besides start, and the instants in rdates, in
// order.
func merge(rule func() (time.Time, bool), loc *time.Location, start, until time.Time, rdates []time.Time) func() (time.Time, bool) {
	slices.SortFunc(rdates, time.Time.Compare)
	var pending time.Time
	waiting, done := false, false
	return func() (time.Time, bool) {
		for !waiting && !done {
			w, ok := rule()
			pending, waiting, done = resolve(w, loc), ok, !ok
			if waiting && !until.IsZero() && pending.After(until) && !pending.Equal(start) {
				waiting = false
			}
		}
		if len(rdates) > 0 && (!waiting || rdates[0].Before(pending)) {
			t := rdates[0]
			rdates = rdates[1:]
			return t, true
		}
		if waiting {
			waiting = false
			return pending, true
		}
		return time.Time{}, false
	}
}

// rule is e's recurrence rule, starting at start, when the clock
// showed first, or nil if it has none, one the panel does not follow,
// or one whose occurrences all end by from. Then only the start and the
// RDATEs are occurrences. The rule runs on the clock, and until, if not
// zero, is the instant its times read in the zone must not pass. Times
// after to are not needed.
func rule(e *ical.Event, first, start time.Time, length span, allDay bool, from, to time.Time) (_ *rrule.RRule, until time.Time) {
	prop := e.Props.Get(ical.PropRecurrenceRule)
	if prop == nil {
		return nil, time.Time{}
	}
	// Rule parts are case-insensitive, and the recurrence library takes
	// neither empty parts nor extensions.
	var parts []string
	for part := range strings.SplitSeq(strings.ToUpper(prop.Value), ";") {
		if part != "" && !strings.HasPrefix(part, "X-") {
			parts = append(parts, part)
		}
	}
	value := strings.Join(parts, ";")
	// An UNTIL with no zone is in the start's.
	loc := start.Location()
	opt, err := rrule.StrToROptionInLocation(value, loc)
	// Rules more frequent than hourly, by their frequency or by more
	// times a day than hours, are not calendar events, and following
	// them would take too long.
	if err != nil || opt.Freq == rrule.MINUTELY || opt.Freq == rrule.SECONDLY || frequent(opt) ||
		// No calendar writes BYSETPOS on these, and one the set never
		// reaches is a rule that can never match.
		len(opt.Bysetpos) > 0 && (opt.Freq == rrule.HOURLY || opt.Freq == rrule.DAILY) || !hoursReachable(opt, first.Hour()) {
		return nil, time.Time{}
	}
	opt.Dtstart = first
	if !opt.Until.IsZero() {
		w := wall(opt.Until.In(loc))
		until = opt.Until
		if opt.Until.Location() != time.UTC {
			until = resolve(w, loc)
		}
		if !allDay && untilIsDate(value) {
			// A date as UNTIL, which some feeds give a series with start
			// times, includes that day.
			w = w.AddDate(0, 0, 1).Add(-time.Second)
			until = resolve(w, loc)
		}
		// Most series in an old feed have ended; following them from
		// their start would take up the feed's time.
		if !length.end(until, allDay).After(from) {
			return nil, time.Time{}
		}
		// Around a clock change, the clock shows a time before UNTIL
		// for hours after it.
		opt.Until = w.Add(3 * time.Hour)
	} else {
		// With no UNTIL, the recurrence library stops about 290 years
		// after the start, and feeds start birthdays of unknown year in
		// 1604.
		opt.Until = wall(to.In(loc)).Add(48 * time.Hour)
	}
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, time.Time{}
	}
	// The start counts toward COUNT even when the rule does not match it;
	// a count of 0 would mean no limit.
	if next, ok := r.Iterator()(); opt.Count > 0 && (!ok || !next.Equal(first)) {
		if opt.Count--; opt.Count == 0 {
			return nil, time.Time{}
		}
		if r, err = rrule.NewRRule(*opt); err != nil {
			return nil, time.Time{}
		}
	}
	return r, until
}

// frequent reports whether a rule gives more than one time an hour, on
// average: more than one a time in an hourly rule, or more than 24 a day
// in the others.
func frequent(opt *rrule.ROption) bool {
	n := max(1, len(opt.Byminute)) * max(1, len(opt.Bysecond))
	if opt.Freq == rrule.HOURLY {
		return n > 1
	}
	return n*max(1, len(opt.Byhour)) > 24
}

// appendIn appends ev to events if it ends after from and starts before
// to.
func appendIn(events []event, ev event, from, to time.Time) []event {
	if ev.end.After(from) && ev.start.Before(to) {
		events = append(events, ev)
	}
	return events
}

// describe is what every occurrence of e says, its key without a time.
// The text is decoded once, as a feed may give an event a description
// megabytes long.
func (f feed) describe(e *ical.Event) event {
	return event{
		key:      identity(e),
		title:    cmp.Or(clip(text(e.Props, ical.PropSummary), maxText), "(no title)"),
		location: clip(text(e.Props, ical.PropLocation), maxText),
		notes:    clip(text(e.Props, ical.PropDescription), maxNotes),
	}
}

// at is the occurrence of the event e describes that starts at start.
func (e event) at(start time.Time, length span, allDay bool) event {
	e.key += keyTime(start)
	e.start, e.end, e.allDay = start, length.end(start, allDay), allDay
	return e
}

// occurrenceKey identifies the occurrence of e that starts at t, or,
// for a moved occurrence, started there before it moved.
func occurrenceKey(e *ical.Event, t time.Time) string { return identity(e) + keyTime(t) }

// identity identifies e among the feed's events.
func identity(e *ical.Event) string {
	uid := text(e.Props, ical.PropUID)
	noUID := uid == ""
	if noUID {
		// Without a UID, an event is known by all it says, so two that
		// differ are kept apart.
		var all []string
		for name, props := range e.Props {
			switch name {
			case ical.PropDateTimeStamp, ical.PropLastModified, ical.PropCreated, ical.PropSequence:
				// These change when the feed is served or the event
				// edited, not what the event is.
				continue
			}
			for _, p := range props {
				// Parameters, such as a TZID, change what a value means.
				var params []string
				for k, v := range p.Params {
					params = append(params, k+"="+strings.Join(v, ","))
				}
				slices.Sort(params)
				all = append(all, name+";"+strings.Join(params, ";")+":"+p.Value)
			}
		}
		slices.Sort(all)
		uid = strings.Join(all, "\n")
	}
	// The key is an action ID in every view, so a long UID, which a feed
	// may make as long as it likes, is hashed, as are raw values, which
	// need not be UTF-8.
	if noUID || len(uid) > 64 {
		sum := sha256.Sum256([]byte(uid))
		uid = hex.EncodeToString(sum[:16])
	}
	return uid
}

// keyTime is the part of an occurrence's key that says when it starts.
func keyTime(t time.Time) string { return "@" + t.UTC().Format("20060102T150405Z") }

// span is how long an event lasts: days, then d. Days are calendar
// days, not 24 hours, so a day with a clock change is still a day.
type span struct {
	days int
	d    time.Duration
}

// end is when an occurrence starting at start ends. An all-day event
// lasts whole days.
func (l span) end(start time.Time, allDay bool) time.Time {
	if allDay {
		// At midnight, even if the day started later, when the clocks
		// skipped its midnight.
		y, m, d := start.Date()
		return resolve(time.Date(y, m, d+l.days, 0, 0, 0, 0, time.UTC), start.Location())
	}
	if l.days == 0 {
		return start.Add(l.d)
	}
	return resolve(wall(start).AddDate(0, 0, l.days), start.Location()).Add(l.d)
}

// zones reads the times in a feed, with the time zones they name.
type zones struct {
	loc   *time.Location
	known map[string]*time.Location
}

// span returns when e starts, how long it lasts, and whether it lasts
// whole days.
func (z *zones) span(e *ical.Event) (time.Time, span, bool, error) {
	prop := e.Props.Get(ical.PropDateTimeStart)
	if prop == nil {
		return time.Time{}, span{}, false, fmt.Errorf("no start")
	}
	start, allDay, err := z.time(prop)
	if err != nil {
		return time.Time{}, span{}, false, err
	}
	var length span
	if allDay {
		length.days = 1
	}
	switch {
	case e.Props.Get(ical.PropDateTimeEnd) != nil:
		end, _, err := z.time(e.Props.Get(ical.PropDateTimeEnd))
		if err != nil {
			return time.Time{}, span{}, false, err
		}
		if allDay {
			// Some feeds end a one-day event on the day it starts.
			length.days = max(dayNumber(end)-dayNumber(start), 1)
		} else {
			length.d = max(end.Sub(start), 0)
		}
	case e.Props.Get(ical.PropDuration) != nil:
		days, d, err := nominal(e.Props.Get(ical.PropDuration).Value)
		if err != nil {
			return time.Time{}, span{}, false, err
		}
		if allDay {
			length.days = max(days+int((d+12*time.Hour)/(24*time.Hour)), 1)
		} else {
			length = span{days: days, d: d}
		}
	}
	return start, length, allDay, nil
}

// dayNumber counts the days from the year 1 to t's date.
func dayNumber(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// time reads a date or a date and time, and reports whether it is a
// date. One in a time zone the database does not know, such as a
// Windows name, is read in z.loc, as is one with no zone.
func (z *zones) time(prop *ical.Prop) (time.Time, bool, error) {
	w, loc, date, err := z.clock(prop)
	return resolve(w, loc), date, err
}

// clock reads a date or a date and time as the time on the clock, in
// UTC, and the zone of that clock, and reports whether it is a date.
func (z *zones) clock(prop *ical.Prop) (w time.Time, loc *time.Location, date bool, err error) {
	// Its letters may be in either case.
	value := strings.ToUpper(strings.TrimSpace(prop.Value))
	switch {
	case len(value) == len("20060102"):
		w, err = time.Parse("20060102", value)
		return w, z.loc, true, err
	case strings.HasSuffix(value, "Z"):
		w, err = time.Parse("20060102T150405Z", value)
		return w, time.UTC, false, err
	}
	loc = z.loc
	if tzid := prop.Params.Get(ical.ParamTimezoneID); tzid != "" {
		loc = z.zone(tzid)
	}
	w, err = time.Parse("20060102T150405", value)
	return w, loc, false, err
}

// wall is the time on the clock at t, in t's zone, as a time in UTC.
func wall(t time.Time) time.Time {
	y, m, d := t.Date()
	h, mi, sec := t.Clock()
	return time.Date(y, m, d, h, mi, sec, 0, time.UTC)
}

// resolve is the instant when the clock in loc shows w, a time in UTC,
// as RFC 5545 (3.3.5, with erratum 4271) reads it: a time the clocks go
// back past comes twice and is the first, and one they skip is read
// with the offset before the gap. Go's time.Date picks either.
func resolve(w time.Time, loc *time.Location) time.Time {
	if loc == time.UTC {
		return w
	}
	// Offsets are within a day of UTC.
	_, before := w.Add(-24 * time.Hour).In(loc).Zone()
	_, after := w.Add(24 * time.Hour).In(loc).Zone()
	first := w.Add(-time.Duration(before) * time.Second).In(loc)
	if before == after || wall(first).Equal(w) {
		return first
	}
	if second := w.Add(-time.Duration(after) * time.Second).In(loc); wall(second).Equal(w) {
		return second
	}
	return first
}

// stamp is a date and time, or a date, and for a period, its length.
type stamp struct {
	t      time.Time
	date   bool
	length time.Duration
	period bool
}

// times reads the dates in props, each of which may hold several
// separated by commas, and leaves out any it cannot read, such as
// periods.
func (z *zones) times(props []ical.Prop) []stamp {
	var all []stamp
	for _, prop := range props {
		for value := range strings.SplitSeq(prop.Value, ",") {
			p := prop
			value, until, period := strings.Cut(strings.TrimSpace(value), "/")
			p.Value = value
			t, date, err := z.time(&p)
			if err != nil {
				continue
			}
			s := stamp{t: t, date: date, period: period}
			if period {
				// A period ends at a time, or lasts a duration.
				end := p
				end.Value = until
				if strings.ContainsAny(until, "Pp") {
					d := ical.Prop{Name: ical.PropDuration, Params: ical.Params{}, Value: until}
					s.length, err = d.Duration()
				} else {
					var last time.Time
					last, _, err = z.time(&end)
					s.length = last.Sub(t)
				}
				if err != nil || s.length < 0 {
					continue
				}
			}
			all = append(all, s)
		}
	}
	return all
}

// zone returns the time zone named tzid, an IANA name or a Windows one,
// or z.loc if neither is known. Some feeds put a prefix of their own before the IANA
// name, such as /mozilla.org/20050126_1/Europe/Berlin, which is dropped
// one segment at a time until a name is known.
func (z *zones) zone(tzid string) *time.Location {
	if loc, ok := z.known[tzid]; ok {
		return loc
	}
	loc := z.loc
	name := tzid
	if iana, ok := windowsZones[strings.TrimSpace(tzid)]; ok {
		name = iana
	}
	for name = strings.Trim(name, "/"); name != ""; {
		if l, err := time.LoadLocation(name); err == nil && name != "Local" {
			loc = l
			break
		}
		_, rest, ok := strings.Cut(name, "/")
		if !ok {
			break
		}
		name = rest
	}
	z.known[tzid] = loc
	return loc
}

// text reads the text property name, with its escapes undone. Commas
// that are not escaped are kept, since some feeds do not escape them.
func text(props ical.Props, name string) string {
	prop := props.Get(name)
	if prop == nil {
		return ""
	}
	var b strings.Builder
	escaped := false
	for _, r := range prop.Value {
		switch {
		case escaped && (r == 'n' || r == 'N'):
			b.WriteByte('\n')
		case escaped, r != '\\':
			b.WriteRune(r)
		}
		escaped = !escaped && r == '\\'
	}
	return strings.TrimSpace(b.String())
}

// clip cuts s to n characters, marking the cut with an ellipsis.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n])) + "…"
	}
	return s
}

// hoursReachable reports whether an hourly rule starting at hour start
// reaches one of its BYHOUR hours. The recurrence library would look for
// one forever: stepping by the interval, it only ever reaches the hours
// start plus a multiple of the interval's greatest common divisor with
// 24.
func hoursReachable(opt *rrule.ROption, start int) bool {
	if opt.Freq != rrule.HOURLY || len(opt.Byhour) == 0 {
		return true
	}
	step := 24
	for b := max(opt.Interval, 1) % 24; b != 0; {
		step, b = b, step%b
	}
	for _, h := range opt.Byhour {
		if ((h-start)%step+step)%step == 0 {
			return true
		}
	}
	return false
}

// untilIsDate reports whether the recurrence rule rule ends on a date
// rather than a date and time.
func untilIsDate(rule string) bool {
	for part := range strings.SplitSeq(rule, ";") {
		if name, value, ok := strings.Cut(part, "="); ok && strings.EqualFold(name, "UNTIL") {
			return len(strings.TrimSpace(value)) == len("20060102")
		}
	}
	return false
}

// nominal reads an iCalendar duration, such as P1DT2H or P2W, as whole
// calendar days and the rest. A negative one is none.
func nominal(value string) (days int, d time.Duration, err error) {
	s := strings.ToUpper(strings.TrimSpace(value))
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	rest, ok := strings.CutPrefix(s, "P")
	if !ok || rest == "" {
		return 0, 0, fmt.Errorf("invalid duration %q", value)
	}
	inTime := false
	for rest != "" {
		if rest[0] == 'T' {
			inTime, rest = true, rest[1:]
			continue
		}
		i := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
		if i <= 0 {
			return 0, 0, fmt.Errorf("invalid duration %q", value)
		}
		n, err := strconv.Atoi(rest[:i])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid duration %q", value)
		}
		switch unit := rest[i]; {
		case !inTime && unit == 'W':
			days += 7 * n
		case !inTime && unit == 'D':
			days += n
		case inTime && unit == 'H':
			d += time.Duration(n) * time.Hour
		case inTime && unit == 'M':
			d += time.Duration(n) * time.Minute
		case inTime && unit == 'S':
			d += time.Duration(n) * time.Second
		default:
			return 0, 0, fmt.Errorf("invalid duration %q", value)
		}
		rest = rest[i+1:]
	}
	if negative {
		return 0, 0, nil
	}
	return days, d, nil
}
