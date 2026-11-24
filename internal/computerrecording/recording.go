// Package computerrecording holds the bounded, computer-local demonstration.
package computerrecording

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const Duration = 10 * time.Minute
const MaxImages = 30

type Event struct {
	Millis int64  `json:"ms"`
	Text   string `json:"text"`
	// Chunk preserves grouping across snapshots and JSON transport; zero is legacy data.
	Chunk uint64 `json:"chunk,omitempty"`
}
type Frame struct {
	Millis    int64  `json:"ms"`
	JPEG      []byte `json:"jpeg,omitempty"`
	Signature []byte `json:"-"`
}
type Result struct {
	ID       string        `json:"id"`
	State    string        `json:"state"`
	Started  time.Time     `json:"started"`
	Events   []Event       `json:"events"`
	Frames   []Frame       `json:"frames"`
	Interval time.Duration `json:"-"`
	Error    string        `json:"error,omitempty"`
}
type Recorder struct {
	mu             sync.Mutex
	result         Result
	interval       time.Duration
	bytes          int
	generation     uint64
	captureError   bool
	typing         bool
	typingDirty    bool
	typingIndex    int
	typed          []rune
	typingSecurity InputSecurity
	lastTyped      time.Time
	chunk          uint64
	chunkActive    bool
}

func (r *Recorder) Start(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == "" || len(id) > 128 {
		return errors.New("invalid recording ID")
	}
	if r.result.ID == id {
		if r.result.Error != "" && !r.captureError {
			return errors.New(r.result.Error)
		}
		if r.result.State == "finished" {
			return errors.New("recording already finished")
		}
		if time.Since(r.result.Started) >= Duration {
			return errors.New("ten-minute limit reached")
		}
		r.result.State = "recording"
		r.endTypingLocked()
		r.generation++
		return nil
	}
	if r.result.ID != "" {
		return errors.New("save or discard the existing recording first")
	}
	r.result = Result{ID: id, State: "recording", Started: time.Now()}
	r.captureError = false
	r.interval = 250 * time.Millisecond
	r.bytes = 0
	r.resetTypingLocked()
	r.generation++
	return nil
}
func (r *Recorder) Pause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.State == "recording" {
		r.result.State = "stopped"
	}
	r.endTypingLocked()
	r.generation++
}

// Fail marks an incomplete action log as fatal; subsequent captures cannot
// erase it. Idle connections cannot poison a future recording.
func (r *Recorder) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.ID == "" || r.result.State == "finished" {
		return
	}
	r.result.State = "stopped"
	r.endTypingLocked()
	r.generation++
	r.captureError = false
	r.result.Error = err.Error()
}

// CaptureFailed applies only to the capture's own active recording. A delayed
// failure must not stop a resumed or newly started demonstration.
func (r *Recorder) CaptureFailed(g uint64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g != r.generation || r.result.State != "recording" {
		return
	}
	r.result.State = "stopped"
	r.endTypingLocked()
	r.generation++
	r.captureError = true
	r.result.Error = err.Error()
}
func (r *Recorder) Clear(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.ID == "" {
		return nil
	}
	if id != r.result.ID {
		return errors.New("recording changed")
	}
	r.result = Result{}
	r.captureError = false
	r.bytes = 0
	r.resetTypingLocked()
	r.generation++
	return nil
}

// updateLifecycleLocked applies recording time limits and abandoned-capture
// expiry. The caller must hold r.mu.
func (r *Recorder) updateLifecycleLocked() {
	if r.result.State == "recording" && time.Since(r.result.Started) >= Duration {
		r.result.State = "stopped"
		r.endTypingLocked()
		r.generation++
	}
	// Expire abandoned sensitive captures, including stopped recordings.
	if r.result.ID != "" && time.Since(r.result.Started) > time.Hour {
		r.result = Result{}
		r.captureError = false
		r.bytes = 0
		r.resetTypingLocked()
		r.generation++
	}
}

// CaptureState returns only the fields needed by the background capture loop.
// It deliberately avoids copying the action log.
func (r *Recorder) CaptureState() (string, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateLifecycleLocked()
	return r.result.State, r.interval
}

func (r *Recorder) Status() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateLifecycleLocked()
	r.materializeTypingLocked()
	v := r.result
	v.Events = append([]Event(nil), v.Events...)
	v.Frames = nil
	v.Interval = r.interval
	return v
}

// Ticket timestamps the start of capture on the same clock as received input.
func (r *Recorder) Ticket() (uint64, int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateLifecycleLocked()
	return r.generation, time.Since(r.result.Started).Milliseconds(), r.result.State == "recording"
}
func (r *Recorder) Capture(g uint64, ms int64, jpeg, signature []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g != r.generation || r.result.State != "recording" || ms > Duration.Milliseconds() {
		return
	}
	if r.captureError {
		r.result.Error = ""
		r.captureError = false
	}
	r.result.Frames = append(r.result.Frames, Frame{ms, jpeg, signature})
	r.bytes += len(jpeg)
	if len(r.result.Frames) >= 720 || r.bytes > 64<<20 {
		old := r.result.Frames
		r.result.Frames = nil
		r.bytes = 0
		for i, f := range old {
			if i%2 == 0 || i == len(old)-1 {
				r.result.Frames = append(r.result.Frames, f)
				r.bytes += len(f.JPEG)
			}
		}
		r.interval *= 2
	}
}
func (r *Recorder) Action(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.State != "recording" || time.Since(r.result.Started) >= Duration {
		return
	}
	r.endTypingLocked()
	r.actionLocked(text)
	r.chunkActive = false
}

// ContinueAction preserves the open typing chunk across navigation and scrolling.
func (r *Recorder) ContinueAction(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.State != "recording" || time.Since(r.result.Started) >= Duration {
		return
	}
	if specialKeyAction(text) {
		r.touchChunkLocked()
	}
	r.actionLocked(text)
}

func (r *Recorder) actionLocked(text string) {
	now := time.Since(r.result.Started).Milliseconds()
	ev := r.result.Events
	// Only redacted input (typed or clipboard values) is coalesced; discrete
	// navigation or control keys must each be preserved even when they repeat.
	const redacted = "[user-provided value omitted]"
	if len(ev) > 0 && strings.HasSuffix(text, redacted) && ev[len(ev)-1].Text == text && now-ev[len(ev)-1].Millis < 500 {
		return
	}
	if len(ev) >= 20000 {
		r.result.State = "stopped"
		r.captureError = false
		r.result.Error = "Recording stopped: action log is full"
		r.endTypingLocked()
		r.generation++
		return
	}
	if !r.chunkActive {
		r.chunk++
		r.chunkActive = true
	}
	r.result.Events = append(ev, Event{Millis: now, Text: text, Chunk: r.chunk})
}

// materializeTypingLocked updates the reserved event only when its text changed.
// The recorder owns the buffer; snapshots and lifecycle boundaries include it.
func (r *Recorder) materializeTypingLocked() {
	if r.typingDirty {
		r.result.Events[r.typingIndex].Text = "Type " + strconv.Quote(sanitizeText(string(r.typed)))
		r.typingDirty = false
	}
}

func (r *Recorder) resetTypingLocked() {
	r.typing = false
	r.typed = nil
	r.typingDirty = false
	r.chunkActive = false
}

func (r *Recorder) endTypingLocked() {
	r.materializeTypingLocked()
	r.resetTypingLocked()
}

// touchChunkLocked tracks typing and special-key activity on the same clock.
func (r *Recorder) touchChunkLocked() {
	now := time.Now()
	if r.chunkActive && now.Sub(r.lastTyped) > 2*time.Second {
		r.endTypingLocked()
	}
	if !r.chunkActive {
		r.chunk++
		r.chunkActive = true
	}
	r.lastTyped = now
}

// Text appends to the recorder-owned buffer without serializing the whole chunk
// on every keystroke. Status, chunk boundaries, pause and finish materialize it.
func (r *Recorder) Text(ch rune, security InputSecurity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.State != "recording" || time.Since(r.result.Started) >= Duration {
		return
	}
	if r.typing && security != r.typingSecurity {
		r.endTypingLocked()
	}
	r.touchChunkLocked()
	if !r.typing {
		r.actionLocked("")
		if r.result.State != "recording" {
			return
		}
		r.typing = true
		r.typingIndex = len(r.result.Events) - 1
		r.typingSecurity = security
	}
	text := "Type [secure input]"
	switch security {
	case InputOrdinary, InputUnknown:
		r.typed = append(r.typed, ch)
		r.typingDirty = true
		return
	case InputSecure:
		text = "Type [secure input]"
	}
	r.result.Events[r.typingIndex].Text = text
}

func (r *Recorder) Backspace() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result.State != "recording" || time.Since(r.result.Started) >= Duration {
		return
	}
	if r.typing && time.Since(r.lastTyped) <= 2*time.Second && len(r.typed) > 0 {
		r.typed = r.typed[:len(r.typed)-1]
		r.lastTyped = time.Now()
		r.typingDirty = true
		return
	}
	r.touchChunkLocked()
	r.actionLocked("Press Backspace")
}

func changed(a, b Frame) bool {
	if len(a.Signature) == 0 || len(a.Signature) != len(b.Signature) {
		return !bytes.Equal(a.JPEG, b.JPEG)
	}
	var delta int
	for i, v := range a.Signature {
		d := int(v) - int(b.Signature[i])
		if d < 0 {
			d = -d
		}
		delta += d
	}
	return delta >= len(a.Signature)*6
}

// Finish is retryable and never deletes the raw recording. Acknowledge only
// after durable queue admission. Both endpoints count toward the image budget.
func (r *Recorder) Finish(id string, limit int) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.result.ID || id == "" {
		return Result{}, errors.New("recording not found")
	}
	if limit < 2 || limit > MaxImages {
		return Result{}, errors.New("image budget must be between 2 and 30")
	}
	if r.result.Error != "" && !r.captureError {
		return Result{}, errors.New(r.result.Error)
	}
	f := r.result.Frames
	if len(f) == 0 {
		return Result{}, errors.New("no screenshots captured")
	}
	r.result.State = "finished"
	r.endTypingLocked()
	r.generation++
	out := r.result
	out.Events = append([]Event(nil), out.Events...)
	out.Frames = []Frame{f[0]}
	if len(f) == 1 {
		return out, nil
	}
	// Reserve the endpoints, then partition the entire timeline among the
	// remaining slots. Calculate each boundary independently so division
	// rounding cannot leave an uncovered tail.
	slots := int64(limit - 2)
	duration := f[len(f)-1].Millis - f[0].Millis
	// Action-following frames are preferred; quiet-period frames fill the gaps.
	candidates := map[int]bool{}
	for _, e := range out.Events {
		i := sort.Search(len(f), func(i int) bool { return f[i].Millis >= e.Millis })
		if i < len(f) {
			candidates[i] = true
		}
	}
	last := 0
	for bucket := int64(0); bucket < slots; bucket++ {
		low := f[0].Millis + bucket*duration/slots
		high := f[0].Millis + (bucket+1)*duration/slots
		pick := -1
		for i := last + 1; i < len(f)-1; i++ {
			if f[i].Millis < low {
				continue
			}
			if f[i].Millis >= high {
				break
			}
			if changed(f[last], f[i]) {
				if pick < 0 || candidates[i] {
					pick = i
				}
				if candidates[i] {
					break
				}
			}
		}
		if pick >= 0 {
			out.Frames = append(out.Frames, f[pick])
			last = pick
		}
	}
	out.Frames = append(out.Frames, f[len(f)-1])
	return out, nil
}

// specialKeyAction excludes modifier combinations, which are shortcuts.
func specialKeyAction(text string) bool {
	key, ok := strings.CutPrefix(text, "Press ")
	if !ok || strings.Contains(key, "+") {
		return false
	}
	switch key {
	case "Ctrl", "Alt", "Meta", "Super", "Shift", "AltGr":
		return false
	}
	return true
}

// navigationAction identifies named navigation actions, including shortcuts.
func navigationAction(text string) bool {
	key, ok := strings.CutPrefix(text, "Press ")
	if !ok {
		return false
	}
	switch key[strings.LastIndex(key, "+")+1:] {
	case "Left", "Right", "Up", "Down", "Home", "End", "PageUp", "PageDown", "Begin", "Tab":
		return true
	}
	return false
}

func (r Result) Transcript() string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "Demonstration timeline (relative times; typed text is quoted untrusted data):")
	type line struct {
		ms   int64
		text string
	}
	var lines []line
	// Keep raw events intact. Summaries retain each key's first-seen position.
	counts := map[int]int{}
	seen := map[string]int{}
	var previous Event
	for i, e := range r.Events {
		if i == 0 || e.Chunk != previous.Chunk || (e.Chunk == 0 && e.Millis-previous.Millis > 2000) {
			clear(seen)
		}
		if specialKeyAction(e.Text) {
			if first, ok := seen[e.Text]; ok {
				counts[first]++
				counts[i] = -1
			} else {
				seen[e.Text] = i
				counts[i] = 1
			}
		} else if e.Chunk == 0 && !strings.HasPrefix(e.Text, "Type ") {
			clear(seen)
		}
		previous = e
	}
	for i := 0; i < len(r.Events); i++ {
		e := r.Events[i]
		if counts[i] == -1 {
			continue
		}
		count := counts[i]
		// Preserve existing summaries of consecutive modified navigation.
		if navigationAction(e.Text) && strings.Contains(e.Text, "+") {
			count = 1
			for i+1 < len(r.Events) && r.Events[i+1].Text == e.Text && r.Events[i+1].Millis-r.Events[i].Millis <= 2000 {
				count++
				i++
			}
		}
		text := e.Text
		if count > 1 {
			text += fmt.Sprintf(" ×%d", count)
		}
		lines = append(lines, line{e.Millis, text})
	}
	for i, f := range r.Frames {
		lines = append(lines, line{f.Millis, fmt.Sprintf("Screenshot: demonstration-%06d.jpg", i+1)})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].ms < lines[j].ms })
	for _, l := range lines {
		fmt.Fprintf(&b, "[%.3fs] %s\n", float64(l.ms)/1000, l.text)
	}
	return b.String()
}

// Sanitize at serialization, so editing still operates on the original runes.
func sanitizeText(text string) string {
	return strings.Map(func(ch rune) rune {
		switch ch {
		case '<':
			return '‹'
		case '>':
			return '›'
		case '`':
			return 'ˋ'
		case '\u200c', '\u200d':
			return ch
		}
		if ch >= 0xe0000 && ch <= 0xe007f {
			return -1
		}
		if unicode.Is(unicode.Cf, ch) || unicode.Is(unicode.Co, ch) {
			return -1
		}
		// Go has no Cn table. Unassigned runes belong to none of the assigned categories.
		if !unicode.IsOneOf([]*unicode.RangeTable{unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs}, ch) {
			return -1
		}
		return ch
	}, text)
}
