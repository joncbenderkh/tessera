// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

// Command processes is the top processes panel: a table of the
// processes using the most CPU, or sorted another way, with their
// memory. Activating a row shows the process's details below the
// table; activating it again hides them. The sort, the number of rows,
// and how often it samples are settings.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	"github.com/joncbenderkh/tessera/sdk"
)

const (
	defaultInterval = "2s"
	defaultSort     = "cpu"
	defaultRows     = 10
	maxRows         = 100
)

var schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "sort": {
      "enum": ["cpu", "memory", "pid", "name"],
      "description": "What to sort by: the most CPU or memory first, or by process ID or name.",
      "default": "` + defaultSort + `"
    },
    "rows": {
      "type": "integer",
      "minimum": 1,
      "maximum": ` + strconv.Itoa(maxRows) + `,
      "description": "How many processes to show.",
      "default": ` + strconv.Itoa(defaultRows) + `
    },
    ` + metric.IntervalSchema(defaultInterval) + `
  },
  "additionalProperties": false
}`

type settings struct {
	Sort     string `json:"sort"`
	Rows     int    `json:"rows"`
	Interval string `json:"interval"`
}

// sortKey is a way to sort the table: its column, the indicator's
// order, the title's wording, and the comparison.
type sortKey struct {
	column int
	order  sdk.SortOrder
	title  string
	cmp    func(a, b proc) int
}

var sorts = map[string]sortKey{
	"cpu": {2, sdk.SortDescending, "Top processes by CPU", func(a, b proc) int {
		return cmp.Compare(orLess(b.cpu), orLess(a.cpu))
	}},
	"memory": {3, sdk.SortDescending, "Top processes by memory", func(a, b proc) int {
		return cmp.Compare(b.rss, a.rss)
	}},
	"pid": {1, sdk.SortAscending, "Processes by ID", func(a, b proc) int {
		return 0
	}},
	"name": {0, sdk.SortAscending, "Processes by name", func(a, b proc) int {
		return cmp.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	}},
}

// orLess is v, or -1, below every percentage, if it is unknown.
func orLess(v float64) float64 {
	if math.IsNaN(v) {
		return -1
	}
	return v
}

// proc is one process as the table shows it. cpu is the share of all
// the CPUs' time it used since the previous sample, in percent, NaN
// if unknown, as for a process new since then; rss is its resident
// memory.
type proc struct {
	pid  int32
	name string
	cpu  float64
	rss  uint64
	// start is when the process started, in clock ticks since boot,
	// which tells it from a later one given its ID.
	start uint64
}

// cpuTime is a process's CPU time in seconds at a sample, with its
// start, which tells a process from a later one given its ID.
type cpuTime struct {
	start   uint64
	seconds float64
}

// details is what the panel shows of the selected process.
type details struct {
	pid    int32
	exited bool
	// unreadable is whether reading the details took too long.
	unreadable bool
	name       string
	parent     int32
	user       string
	status     string
	threads    int32
	started    time.Time
	cmdline    string
}

// selection is the process whose details show: its ID, and its start
// in clock ticks since boot, so a later process given the same ID is
// not taken for it.
type selection struct {
	pid   int32
	start uint64
}

// processes is the panel's state. Configure, Activate, and the poller
// run concurrently, so mu guards it. Polling starts on the first
// Configure, so no view shows a sort the settings do not ask for.
type processes struct {
	views   chan<- sdk.View
	poller  metric.Poller
	sampler metric.Sampler
	// lookupLimit, if set, is how long a lookup may take, rather than
	// the sample limit.
	lookupLimit time.Duration
	cores       int

	mu       sync.Mutex
	sort     string
	rows     int
	interval time.Duration
	selected selection
	// gen counts the selections made by Activate, so a sample or lookup
	// started before one is not shown over it.
	gen int
	// cancelLookup stops the details lookup last started, for the
	// selection of generation lookupGen, and looking is whether it is
	// still running.
	cancelLookup context.CancelFunc
	lookupGen    int
	looking      bool
	// details are the selected process's, as last read; nil if none
	// have been.
	details *details
	// lookups has each process's own sampler for its details, so a
	// read of one stuck in the kernel leaves at most one stuck with it,
	// and holds up no other's; others are dropped.
	lookups map[int32]*metric.Sampler
	procs   []proc
	times   map[int32]cpuTime
	at      time.Time
	sampled bool
	lastErr string
	// failed is whether the last sample failed, so the one before is
	// not shown as current.
	failed bool
}

func main() {
	views := make(chan sdk.View)
	p := &processes{views: views, cores: 1}
	if n, err := cpu.Counts(true); err == nil && n > 0 {
		p.cores = n
	}
	sdk.Serve(sdk.Plugin{
		Name:         "processes",
		Version:      tessera.Version,
		Description:  "The processes using the most CPU or memory.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 2, Rows: 2},
		Views:        views,
		Configure:    p.configure,
		Activate:     p.activate,
	})
}

func (p *processes) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Sort: defaultSort, Rows: defaultRows, Interval: defaultInterval}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	if _, ok := sorts[s.Sort]; !ok {
		return sdk.InvalidSettings("sort %q is not cpu, memory, pid, or name", s.Sort)
	}
	if s.Rows < 1 || s.Rows > maxRows {
		return sdk.InvalidSettings("rows %d is not between 1 and %d", s.Rows, maxRows)
	}
	interval, err := metric.ParseInterval(s.Interval)
	if err != nil {
		return err
	}
	p.mu.Lock()
	redraw := s.Sort != p.sort || s.Rows != p.rows
	resample := interval != p.interval || !p.sampled
	p.sort, p.rows, p.interval = s.Sort, s.Rows, interval
	if redraw && !resample {
		// Shown again from the last sample, as Activate does.
		p.show(p.details)
	}
	p.mu.Unlock()
	if resample {
		p.poller.Start(p.poll)
	}
	return nil
}

func (p *processes) activate(_ context.Context, actionID string) error {
	clicked, err := parseAction(actionID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.selected == clicked {
		p.selected = selection{}
	} else {
		p.selected = clicked
	}
	p.details = nil
	p.gen++
	sel, gen, sampled := p.selected, p.gen, p.sampled
	p.mu.Unlock()
	if !sampled {
		return nil
	}

	// The view shows the new selection with the last sample: sampling
	// now would measure the CPU over a moment, and reorder the rows
	// under the pointer. The details are read after the event is
	// answered, so a slow read does not hold it up.
	go p.showLookup(gen, sel)
	return nil
}

// showLookup shows the last sample with the details of sel, the
// selection of generation gen.
func (p *processes) showLookup(gen int, sel selection) {
	p.mu.Lock()
	if gen < p.lookupGen || gen == p.lookupGen && p.looking {
		// Started for an older selection than one already looked up,
		// or one for this selection is running, perhaps stuck, and
		// shows its details when it ends or runs out of time.
		p.mu.Unlock()
		return
	}
	// Only the newest selection's lookup counts, so an earlier one
	// stops.
	if p.cancelLookup != nil {
		p.cancelLookup()
		p.cancelLookup = nil
	}
	if sel.pid == 0 {
		// None selected: nothing to read.
		p.lookupGen, p.looking = gen, false
		p.showFor(gen, sel, nil)
		p.mu.Unlock()
		return
	}
	p.lookupGen, p.looking = gen, true
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelLookup = cancel
	limit := cmp.Or(p.lookupLimit, metric.Limit(p.interval))
	sampler := p.lookupSampler(sel.pid)
	if p.details == nil {
		// A new selection: show the table without the details of the
		// last one while these are read, which may take until the
		// limit.
		p.showFor(gen, sel, nil)
	}
	p.mu.Unlock()
	defer cancel()
	d, err := metric.Sample(ctx, sampler, limit, func(ctx context.Context) (*details, error) {
		return lookup(ctx, sel), nil
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen == p.lookupGen {
		p.looking = false
	}
	switch {
	case errors.Is(err, context.Canceled):
		// A newer selection's lookup took its place, and shows its own
		// details.
		return
	case err != nil:
		d = &details{pid: sel.pid, unreadable: true}
	}
	p.showFor(gen, sel, d)
}

// lookupSampler is the sampler for the details of the process pid;
// those of others, which are not reading, are dropped, after the lookup
// that might start one on them has been canceled. p.mu is held.
func (p *processes) lookupSampler(pid int32) *metric.Sampler {
	if p.lookups == nil {
		p.lookups = map[int32]*metric.Sampler{}
	}
	for id, s := range p.lookups {
		if id != pid && !s.Busy() {
			delete(p.lookups, id)
		}
	}
	s := p.lookups[pid]
	if s == nil {
		s = new(metric.Sampler)
		p.lookups[pid] = s
	}
	return s
}

// showFor shows the last sample with the details d, read for sel, the
// selection of generation gen: unless Activate has made another since,
// and without them if sel has since exited and been cleared. p.mu is
// held.
func (p *processes) showFor(gen int, sel selection, d *details) {
	if gen != p.gen {
		return
	}
	if sel != p.selected {
		d = nil
	}
	p.details = d
	p.show(d)
}

// action is the action ID of the row of pr. It carries the process's
// start, as the row showed it, so a click that lands after the
// process has exited and its ID has been reused is not taken for the
// new process.
func action(pr proc) string {
	return "pid:" + strconv.FormatInt(int64(pr.pid), 10) + ":" + strconv.FormatUint(pr.start, 10)
}

func parseAction(id string) (selection, error) {
	s, ok := strings.CutPrefix(id, "pid:")
	p, c, found := strings.Cut(s, ":")
	pid, err1 := strconv.ParseInt(p, 10, 32)
	start, err2 := strconv.ParseUint(c, 10, 64)
	if !ok || !found || err1 != nil || err2 != nil || pid <= 0 {
		return selection{}, fmt.Errorf("unknown action %q", id)
	}
	return selection{pid: int32(pid), start: start}, nil
}

func (p *processes) poll(ctx context.Context) time.Duration {
	p.mu.Lock()
	interval := p.interval
	last := reading{times: p.times, at: p.at}
	p.mu.Unlock()

	r, err := metric.Sample(ctx, &p.sampler, metric.Limit(interval), func(ctx context.Context) (reading, error) {
		return take(ctx, last, p.cores)
	})
	if ctx.Err() != nil {
		// The settings changed; the poller was woken.
		return 0
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		if err.Error() != p.lastErr {
			log.Print(err)
		}
		p.lastErr = err.Error()
		p.failed = true
		p.views <- failed(sorts[p.sort].title)
		return p.interval
	}
	p.lastErr, p.failed = "", false
	if p.sampled && r.at.Sub(p.at) < metric.MinElapsed {
		// Too soon after the last sample to measure the CPU over, as
		// when settings come while one is taken: keep that one, and
		// wait.
		return metric.MinElapsed
	}
	p.times, p.at, p.procs = r.times, r.at, r.procs
	first := !p.sampled
	p.sampled = true
	p.show(p.details)
	if p.selected.pid != 0 {
		// The details are read apart from the table, so a process
		// stuck in the kernel, whose command line cannot be read, holds
		// up only them, and shown when read.
		go p.showLookup(p.gen, p.selected)
	}
	if first {
		return 500 * time.Millisecond
	}
	return p.interval
}

// show sends the panel's view of the last sample, with the details sel
// of the selected process. A selected process that has exited shows so
// once, and is then no longer selected. p.mu is held.
func (p *processes) show(sel *details) {
	if p.failed {
		// The last sample is out of date; drawing it, as when the sort
		// changes, would hide that.
		p.views <- failed(sorts[p.sort].title)
		return
	}
	key := sorts[p.sort]
	procs := slices.Clone(p.procs)
	sortProcs(procs, key)
	p.views <- view(key, procs[:min(p.rows, len(procs))], len(procs), sel)
	if sel != nil && sel.exited {
		p.selected, p.details = selection{}, nil
	}
}

// reading is a sample of the processes: the table's rows, and each
// one's CPU time when it was taken, at, which the next sample measures
// from.
type reading struct {
	procs []proc
	times map[int32]cpuTime
	at    time.Time
}

// take samples the processes, measuring their CPU use over the time
// since last, on cores CPUs.
func take(ctx context.Context, last reading, cores int) (reading, error) {
	at := time.Now()
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return reading{}, err
	}
	r := reading{
		procs: make([]proc, 0, len(pids)),
		times: make(map[int32]cpuTime, len(pids)),
		at:    at,
	}
	for _, pid := range pids {
		pr, t, ok := sample(pid)
		if !ok {
			continue
		}
		pr.cpu = math.NaN()
		r.times[pid] = t
		if old, seen := last.times[pid]; seen && old.start == t.start {
			pr.cpu = cpuPercent(old.seconds, t.seconds, at.Sub(last.at), cores)
		}
		r.procs = append(r.procs, pr)
	}
	return r, nil
}

// lookup reads the details of the selected process: nil if there is
// none, and exited if it is gone, or its ID now belongs to another.
func lookup(ctx context.Context, sel selection) *details {
	if sel.pid == 0 {
		return nil
	}
	gone := &details{pid: sel.pid, exited: true}
	ps, err := process.NewProcessWithContext(ctx, sel.pid)
	if err != nil {
		return gone
	}
	st, err := readStat(sel.pid)
	if err != nil || st.start != sel.start {
		return gone
	}
	d := &details{pid: sel.pid, name: valid(nameOf(sel.pid, st.comm))}
	if created, err := st.created(); err == nil {
		d.started = time.UnixMilli(created)
	}
	d.parent, _ = ps.PpidWithContext(ctx)
	// Without cgo the user's name is only found in /etc/passwd, not
	// in LDAP or systemd's dynamic users.
	if user, err := ps.UsernameWithContext(ctx); err == nil {
		d.user = valid(user)
	} else if uids, err := ps.UidsWithContext(ctx); err == nil && len(uids) > 0 {
		d.user = "uid " + strconv.FormatInt(int64(uids[0]), 10)
	}
	if st, err := ps.StatusWithContext(ctx); err == nil && len(st) > 0 {
		d.status = valid(st[0])
	}
	d.threads, _ = ps.NumThreadsWithContext(ctx)
	cmdline, _ := ps.CmdlineWithContext(ctx)
	d.cmdline = truncate(valid(cmdline), maxCmdline)
	// The process may have exited while these were read, and its ID
	// gone to another, whose they would then be.
	if again, err := readStat(sel.pid); err != nil || again.start != st.start {
		return gone
	}
	return d
}

// maxCmdline is the most of a command line the details show, in bytes;
// a Java or Electron one can run to megabytes, sent with every view.
const maxCmdline = 4 << 10

// truncate cuts s to at most n bytes, on a character boundary, and ends
// it with an ellipsis if it was longer.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// valid is s, which the system gave, with any bytes that are not UTF-8
// replaced: a process may name itself anything, and a view with
// invalid UTF-8 is dropped.
func valid(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }

// cpuPercent is the share of the time of cores CPUs, in percent, that
// a process whose CPU time went from before to after seconds used over
// elapsed, or NaN if that is too short to tell.
func cpuPercent(before, after float64, elapsed time.Duration, cores int) float64 {
	if elapsed < metric.MinElapsed || after < before {
		return math.NaN()
	}
	return min((after-before)/elapsed.Seconds()/float64(max(cores, 1))*100, 100)
}

// sortProcs sorts procs by key, then by process ID.
func sortProcs(procs []proc, key sortKey) {
	slices.SortFunc(procs, func(a, b proc) int {
		return cmp.Or(key.cmp(a, b), cmp.Compare(a.pid, b.pid))
	})
}

func view(key sortKey, procs []proc, total int, sel *details) sdk.View {
	table := sdk.Table(
		sdk.TableColumn("Process").Weight(3),
		sdk.TableColumn("PID").AlignEnd(),
		sdk.TableColumn("CPU").AlignEnd(),
		sdk.TableColumn("Memory").AlignEnd().Weight(2),
	).Sort(key.column, key.order).Empty("No processes").ID("processes")
	for _, pr := range procs {
		table.ActionRow(action(pr), pr.name, strconv.FormatInt(int64(pr.pid), 10), cpuCell(pr.cpu), metric.BytesString(pr.rss))
	}
	body := sdk.Column(
		sdk.Weighted(1, table),
		sdk.Text(fmt.Sprintf("%d of %d processes", len(procs), total)).Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
	).Gap(sdk.SpacingSmall)
	if sel != nil {
		body.Add(detailView(sel))
	}
	return sdk.View{Title: key.title, Body: body}
}

// cpuCell formats a CPU share for the table, such as "12.6%", or "—"
// if it is unknown, as for a process new since the last sample.
func cpuCell(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	return metric.Percent(v) + "%"
}

func detailView(d *details) sdk.Widget {
	if d.exited {
		return sdk.Text(fmt.Sprintf("Process %d has exited.", d.pid)).Tone(sdk.ToneMuted)
	}
	if d.unreadable {
		return sdk.Text(fmt.Sprintf("Cannot read the details of process %d.", d.pid)).Tone(sdk.ToneError)
	}
	small := func(s string) sdk.Widget { return sdk.Text(s).Size(sdk.SizeSmall).Tone(sdk.ToneMuted) }
	ids := []string{fmt.Sprintf("PID %d", d.pid)}
	if d.parent > 0 {
		ids = append(ids, fmt.Sprintf("parent %d", d.parent))
	}
	if d.user != "" {
		ids = append(ids, d.user)
	}
	var state []string
	if d.status != "" {
		state = append(state, capitalize(d.status))
	}
	if d.threads > 0 {
		state = append(state, plural(int(d.threads), "thread"))
	}
	if !d.started.IsZero() {
		state = append(state, "started "+started(d.started, time.Now()))
	}
	col := sdk.Column(
		sdk.Text(d.name),
		small(strings.Join(ids, " · ")),
	).Gap(sdk.SpacingExtraSmall)
	if len(state) > 0 {
		col.Add(small(strings.Join(state, " · ")))
	}
	if d.cmdline != "" {
		col.Add(sdk.Text(d.cmdline).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace())
	}
	return col
}

// started formats when a process started: the time if today, and the
// date too if not.
func started(t, now time.Time) string {
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2 15:04")
	}
	return t.Format("Jan 2 2006")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// failed is the view when the processes cannot be listed, under the
// title of the table it stands for, so the tile's title stays put.
func failed(title string) sdk.View {
	return sdk.View{
		Title: title,
		Body: sdk.Column(
			sdk.Text("Cannot list the processes").Tone(sdk.ToneError),
			sdk.Text("Retrying; the details are in the panel's logs.").Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
		).Gap(sdk.SpacingSmall),
	}
}
