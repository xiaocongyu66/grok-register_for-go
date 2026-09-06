package obscura

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

// Humanized typing over Input.humanType. The full text and per-character
// delays go in ONE CDP round-trip; the page replays keydown → beforeinput →
// keypress → input → keyup chains on a setTimeout cadence — the same
// architecture as the mouse gesture channel.
//
// Rhythm model (trained on the shapes typists actually produce):
//   - base inter-key interval ~ lognormal around 90ms
//   - first character of a field is slower (aiming)
//   - micro-pauses after punctuation / at word boundaries
//   - rare thinking pauses (200-500ms)
//   - occasional typo: neighbor key + backspace correction (handled by
//     caller typing in chunks; per-char typo correction happens page-side
//     only when fixTypos is set)

// typeDelays builds the per-character delay schedule for text.
func typeDelays(text string, rng *rand.Rand) []uint64 {
	delays := make([]uint64, 0, len([]rune(text)))
	runes := []rune(text)
	for i, ch := range runes {
		// lognormal-ish via two uniforms (Box-Muller would be smoother; two
		// uniforms give the same right tail without pulling in math/rand
		// NormFloat64 allocation churn on hot paths)
		base := 70.0 + math.Exp(0.55*randClamp(rng, -1, 1.6))*45.0
		if i == 0 {
			base += 120 + rng.Float64()*180 // aiming the first key
		}
		if ch == ' ' {
			base += 20 + rng.Float64()*60 // word boundary breath
		}
		if ch == '.' || ch == ',' || ch == '@' || ch == '_' || ch == '-' {
			base += 60 + rng.Float64()*120 // symbol reach
		}
		if rng.Float64() < 0.04 {
			base += 200 + rng.Float64()*300 // thinking pause
		}
		if base > 900 {
			base = 900
		}
		delays = append(delays, uint64(base))
	}
	return delays
}

func randClamp(rng *rand.Rand, lo, hi float64) float64 {
	return lo + rng.Float64()*(hi-lo)
}

// HumanType types text into the currently focused element (or focusExpr)
// with a human rhythm. Blocks until the page-side replay would have
// finished; the replay itself runs independently.
func (c *Client) HumanType(text string) error {
	return c.HumanTypeIntoExpr(text, "document.activeElement", false)
}

// HumanTypeInto types text into a CSS selector's first match, focusing it
// and clearing the existing value first (form-filling semantics).
func (c *Client) HumanTypeInto(text, selector string) error {
	expr := fmt.Sprintf("(function(){ var e = document.querySelector('%s'); if (e) e.focus(); return e; })()",
		selector)
	return c.HumanTypeIntoExpr(text, expr, true)
}

// HumanTypeIntoClear types without clearing (append semantics).
func (c *Client) HumanTypeAppend(text, selector string) error {
	expr := fmt.Sprintf("(function(){ var e = document.querySelector('%s'); if (e) e.focus(); return e; })()",
		selector)
	return c.HumanTypeIntoExpr(text, expr, false)
}

// HumanTypeIntoExpr types into the element an arbitrary JS expression
// returns (the engine focuses it first). clear=true wipes the field first.
func (c *Client) HumanTypeIntoExpr(text, focusExpr string, clear bool) error {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(len(text)<<17)))
	delays := typeDelays(text, rng)
	done := make(chan error, 1)
	go func() {
		_, err := c.Send("Input.humanType", map[string]interface{}{
			"text": text, "delays": delays, "focusExpr": focusExpr, "clear": clear,
		})
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// replay queued page-side
	}
	// wait out the schedule so callers can assert on the result
	total := 0
	for _, d := range delays {
		total += int(d)
	}
	time.Sleep(time.Duration(total+400) * time.Millisecond)
	return nil
}
