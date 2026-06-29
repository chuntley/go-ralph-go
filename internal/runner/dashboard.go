package runner

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Terminal control sequences for the full-screen live view.
const (
	altEnter   = "\033[?1049h\033[?25l" // switch to alternate screen + hide cursor
	altLeave   = "\033[?25h\033[?1049l" // show cursor + restore the normal screen
	cursorHome = "\033[H"               // move cursor to top-left
	clearEOL   = "\033[K"               // clear to end of line
	clearEOS   = "\033[J"               // clear to end of screen
)

// dashboard renders a compact, self-refreshing status view for parallel auto
// mode: one row per in-flight issue (status, elapsed, latest message) plus a
// short tail of recent events.
//
// On a TTY it runs as a full-screen view in the terminal's alternate buffer
// (like htop/top): each frame is redrawn from a cleared screen, so line
// wrapping, emoji, or resizes can never accumulate into a corrupted/duplicated
// block. On exit the normal screen + scrollback are restored and a one-time
// summary is printed. On a non-TTY (CI, a pipe) it degrades to plain prefixed
// lines: no escape codes, so captured logs stay readable.
type dashboard struct {
	w     io.Writer
	isTTY bool

	mu        sync.Mutex
	rows      []*issueRow
	events    []string // recent notable lines (bounded live tail)
	history   []string // every finish line, for the on-exit summary
	altScreen bool     // currently in the alternate screen buffer
	stop      chan struct{}
	stopped   bool
	completed int
	failed    int
}

// issueRow is one in-flight issue's live state. All access goes through the
// dashboard mutex (set via the methods below), so workers and the render loop
// don't race.
type issueRow struct {
	dash      *dashboard
	num       int
	title     string
	branch    string
	status    string
	message   string
	startedAt time.Time
	done      bool
	// messageFn, when set, supplies a live "latest message" (e.g. the last line
	// of the Claude turn) polled on each render; it wins over the last explicit
	// message when it returns non-empty.
	messageFn func() string
}

func newDashboard(w io.Writer) *dashboard {
	d := &dashboard{w: w, stop: make(chan struct{})}
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			d.isTTY = true
		}
	}
	return d
}

func (d *dashboard) start() {
	if !d.isTTY {
		return
	}
	fmt.Fprint(d.w, altEnter)
	d.mu.Lock()
	d.altScreen = true
	d.mu.Unlock()
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-t.C:
				d.render()
			}
		}
	}()
}

func (d *dashboard) stopRender() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	wasAlt := d.altScreen
	d.altScreen = false
	d.mu.Unlock()

	close(d.stop)
	if wasAlt {
		fmt.Fprint(d.w, altLeave) // restore the user's terminal + scrollback
	}
	d.printSummary()
}

// printSummary writes a persistent recap to the normal screen after the live
// view is gone (the alternate buffer takes its contents with it on exit).
func (d *dashboard) printSummary() {
	d.mu.Lock()
	completed, failed := d.completed, d.failed
	hist := append([]string(nil), d.history...)
	d.mu.Unlock()
	fmt.Fprintf(d.w, "ralph auto stopped · %d done · %d failed\n", completed, failed)
	for _, h := range hist {
		fmt.Fprintf(d.w, "  %s\n", h)
	}
}

// size returns the current terminal width/height, falling back to a sane
// default when stdout isn't a real terminal (tests, redirects).
func (d *dashboard) size() (int, int) {
	if f, ok := d.w.(*os.File); ok {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	return 80, 24
}

func (d *dashboard) addRow(num int, title string, messageFn func() string) *issueRow {
	row := &issueRow{dash: d, num: num, title: sanitize(title), status: "starting", startedAt: time.Now(), messageFn: messageFn}
	d.mu.Lock()
	d.rows = append(d.rows, row)
	d.mu.Unlock()
	if !d.isTTY {
		d.plain(fmt.Sprintf("#%d started: %s", num, title))
	}
	return row
}

// event records a notable, run-level line (issue picked up, merged, failed). In
// TTY mode it joins the bounded live tail; otherwise it prints immediately.
func (d *dashboard) event(line string) {
	line = sanitize(firstLine(line))
	if !d.isTTY {
		d.plain(line)
		return
	}
	d.mu.Lock()
	d.events = append(d.events, fmt.Sprintf("%s  %s", nowHHMMSS(), line))
	if len(d.events) > 6 {
		d.events = d.events[len(d.events)-6:]
	}
	d.mu.Unlock()
}

func (d *dashboard) setCounts(completed, failed int) {
	d.mu.Lock()
	d.completed, d.failed = completed, failed
	d.mu.Unlock()
}

func (d *dashboard) plain(line string) {
	fmt.Fprintf(d.w, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), redactSecrets(line))
}

func (r *issueRow) setStatus(s string) {
	s = sanitize(firstLine(s))
	r.dash.mu.Lock()
	r.status = s
	r.dash.mu.Unlock()
}

func (r *issueRow) setMessageFn(fn func() string) {
	r.dash.mu.Lock()
	r.messageFn = fn
	r.dash.mu.Unlock()
}

func (r *issueRow) setBranch(b string) {
	r.dash.mu.Lock()
	r.branch = b
	r.dash.mu.Unlock()
}

func (r *issueRow) setMessage(m string) {
	m = sanitize(firstLine(m))
	r.dash.mu.Lock()
	r.message = m
	r.dash.mu.Unlock()
}

func (r *issueRow) finish(err error) {
	r.dash.mu.Lock()
	r.done = true
	r.dash.mu.Unlock()
	verb := "merged"
	if err != nil {
		verb = "ended: " + redactSecrets(err.Error())
	}
	line := fmt.Sprintf("#%d %s (%s)", r.num, verb, elapsed(r.startedAt))
	r.dash.recordFinish(line)
	r.dash.removeRow(r)
}

// recordFinish adds a finished-issue line to both the live tail and the
// persistent history shown in the exit summary.
func (d *dashboard) recordFinish(line string) {
	d.event(line)
	stamped := fmt.Sprintf("%s  %s", nowHHMMSS(), sanitize(firstLine(line)))
	d.mu.Lock()
	d.history = append(d.history, stamped)
	if len(d.history) > 20 {
		d.history = d.history[len(d.history)-20:]
	}
	d.mu.Unlock()
}

func (d *dashboard) removeRow(row *issueRow) {
	d.mu.Lock()
	out := d.rows[:0]
	for _, x := range d.rows {
		if x != row {
			out = append(out, x)
		}
	}
	d.rows = out
	d.mu.Unlock()
}

// render repaints the whole view from the top of the alternate screen. Because
// every frame starts from a cleared screen, a line that wraps or contains an
// emoji can never accumulate across frames — the failure mode of the old
// cursor-up approach.
func (d *dashboard) render() {
	if !d.isTTY {
		return
	}
	w, h := d.size()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.altScreen {
		return // torn down between the ticker firing and acquiring the lock
	}
	lines := d.composeLocked(w, h)

	var b strings.Builder
	b.WriteString(cursorHome)
	for _, ln := range lines {
		b.WriteString(clip(ln, w-1)) // clip so nothing wraps and shifts the layout
		b.WriteString(clearEOL)
		b.WriteByte('\n')
	}
	b.WriteString(clearEOS) // wipe anything left from a taller previous frame
	fmt.Fprint(d.w, b.String())
}

// composeLocked builds the full-screen view: a multi-line card per active
// issue (title + elapsed, phase, branch, live message), then a recent-events
// tail. Clamped to maxLines so it never exceeds the screen (which would make
// the alternate buffer scroll). Caller holds d.mu.
func (d *dashboard) composeLocked(w, maxLines int) []string {
	lines := []string{
		fmt.Sprintf("ralph auto · %d working · %d done · %d failed", len(d.rows), d.completed, d.failed),
		"",
	}
	for _, r := range d.rows {
		lines = append(lines, d.rowCardLocked(r, w)...)
		lines = append(lines, "")
	}
	if len(d.events) > 0 {
		lines = append(lines, "recent:")
		for _, ev := range d.events {
			lines = append(lines, "  "+ev)
		}
	}
	// Keep the header + cards region; drop the oldest event lines first if the
	// window is too short to hold everything.
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return lines
}

// rowCardLocked renders one issue as a multi-line card. Caller holds d.mu.
func (d *dashboard) rowCardLocked(r *issueRow, w int) []string {
	title := r.title
	if title == "" {
		title = "(untitled)"
	}
	header := spread(fmt.Sprintf("#%d  %s", r.num, title), elapsed(r.startedAt), w-1)
	card := []string{header}
	if r.status != "" {
		card = append(card, "   "+clip(r.status, w-5))
	}
	if r.branch != "" {
		card = append(card, "   ↳ "+clip(r.branch, w-7))
	}
	msg := r.message
	if r.messageFn != nil {
		if live := sanitize(firstLine(r.messageFn())); live != "" {
			msg = live
		}
	}
	if msg != "" {
		card = append(card, "   ▸ "+clip(msg, w-7))
	}
	return card
}

// spread places left and right on one line of the given width, padding between
// them so right is flush to the edge. If they don't fit, left is clipped.
func spread(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	lr, rr := []rune(left), []rune(right)
	gap := width - len(lr) - len(rr)
	if gap < 1 {
		// Not enough room — clip the left side, keep right visible.
		keep := width - len(rr) - 1
		if keep < 1 {
			return clip(left, width)
		}
		return clip(left, keep) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func nowHHMMSS() string { return time.Now().Format("15:04:05") }

func elapsed(start time.Time) string {
	d := time.Since(start).Round(time.Second)
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			return ln
		}
	}
	return strings.TrimSpace(s)
}

// lastLine returns the last non-empty line of s — the "latest message" shown for
// a live Claude turn (see issueRow.messageFn).
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// sanitize strips control characters (ESC, CR, etc.) and turns tabs into spaces
// so a Claude message containing an escape sequence can't corrupt a frame or
// throw off width accounting. Newlines are already removed upstream by firstLine.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// drop other C0 control chars + DEL
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
