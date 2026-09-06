package obscura

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// Humanized mouse interaction over CDP Input.dispatchMouseEvent.
// Trajectory model follows ghost-cursor: cubic bezier through two randomly
// offset control points, easing with a slow start, and an overshoot that
// corrects back. Every move step streams a mouseMoved event so hover/
// pointermove listeners on the page see a continuous gesture — the exact
// signal CF Turnstile scores on before accepting an invisible challenge.
//
// The plain Click(selector) path stays untouched for callers that don't
// need the gesture.

type mousePoint struct {
	X, Y float64
}

var humanRng = rand.New(rand.NewSource(time.Now().UnixNano()))

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func bezier3(p0, p1, p2, p3 mousePoint, t float64) mousePoint {
	u := 1 - t
	a := u * u * u
	b := 3 * u * u * t
	c := 3 * u * t * t
	d := t * t * t
	return mousePoint{
		X: a*p0.X + b*p1.X + c*p2.X + d*p3.X,
		Y: a*p0.Y + b*p1.Y + c*p2.Y + d*p3.Y,
	}
}

// humanPath builds a bezier trajectory with per-step easing. slowStart
// shapes the speed profile: t^k easing (k>1 = slower start, faster end).
func humanPath(from, to mousePoint, rng *rand.Rand) []mousePoint {
	dist := math.Hypot(to.X-from.X, to.Y-from.Y)
	if dist < 1.5 {
		return []mousePoint{to}
	}
	// Control points offset along the path normal, magnitude up to ~30%
	// of the distance — the "windy curve" look of a real hand.
	dev := dist * 0.22
	nx := -(to.Y - from.Y) / dist
	ny := (to.X - from.X) / dist
	off1 := rng.NormFloat64() * dev * 0.5
	off2 := rng.NormFloat64() * dev * 0.5
	cp1 := mousePoint{
		X: from.X + (to.X-from.X)*0.3 + nx*off1,
		Y: from.Y + (to.Y-from.Y)*0.3 + ny*off1,
	}
	cp2 := mousePoint{
		X: from.X + (to.X-from.X)*0.7 + nx*off2,
		Y: from.Y + (to.Y-from.Y)*0.7 + ny*off2,
	}
	steps := int(dist/9) + 10
	k := 1.2 + rng.Float64()*0.9 // easing exponent per gesture
	pts := make([]mousePoint, 0, steps+1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		te := math.Pow(t, k)
		p := bezier3(from, cp1, cp2, to, te)
		// micro-jitter: a real hand never holds sub-pixel still
		p.X += rng.NormFloat64() * 0.35
		p.Y += rng.NormFloat64() * 0.35
		pts = append(pts, p)
	}
	return pts
}

// dispatchMouse sends one Input.dispatchMouseEvent.
func (c *Client) dispatchMouse(evt string, x, y float64, extra map[string]interface{}) error {
	params := map[string]interface{}{"type": evt, "x": x, "y": y}
	for k, v := range extra {
		params[k] = v
	}
	_, err := c.Send("Input.dispatchMouseEvent", params)
	return err
}

// HumanMoveTo streams the whole trajectory as ONE evaluate: the page-side
// script replays the points on a setTimeout chain (8-22ms cadence), so the
// gesture costs a single CDP round-trip instead of one per point. CDP
// round-trips measured 50-100ms each — per-point sends made a 60-point
// gesture take ~5s, which itself is a bot tell.
func (c *Client) HumanMoveTo(x, y float64) error {
	c.mouseMu.Lock()
	defer c.mouseMu.Unlock()
	from := mousePoint{X: c.mouseX, Y: c.mouseY}
	// 25% overshoot: glide past the target by 6-20px, then correct back.
	overshoot := humanRng.Float64() < 0.25
	target := mousePoint{X: x, Y: y}
	if overshoot {
		dx, dy := x-from.X, y-from.Y
		d := math.Hypot(dx, dy)
		if d > 40 {
			over := 6 + humanRng.Float64()*14
			target = mousePoint{X: x + dx/d*over, Y: y + dy/d*over}
		}
	}
	pts := humanPath(from, target, humanRng)
	if overshoot {
		pts = append(pts, humanPath(target, mousePoint{X: x, Y: y}, humanRng)...)
	}
	// per-step delays
	arr := make([][]float64, len(pts))
	total := 0
	for i := range pts {
		d := 8 + humanRng.Intn(15)
		total += d
		arr[i] = []float64{pts[i].X, pts[i].Y, float64(d)}
	}
	if err := c.evaluateGesture(arr); err != nil {
		return err
	}
	c.mouseX, c.mouseY = x, y
	time.Sleep(time.Duration(total) * time.Millisecond)
	return nil
}

// evaluateGesture streams one mouseMoved chain into the page via CDP
// Input.humanGesture (single round-trip; the page replays the points on a
// setTimeout chain so event timestamps carry real inter-event gaps).
func (c *Client) evaluateGesture(points [][]float64) error {
	params := map[string]interface{}{"points": points}
	done := make(chan error, 1)
	go func() {
		_, err := c.Send("Input.humanGesture", params)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return nil // page-side replay is already queued
	}
}

// HumanClick moves to (x,y) and presses/releases with a human press
// duration (50-120ms) and settle pause before the click. Press/release run
// in a fire-and-forget goroutine: the click effect is the dispatched page
// events, and obscura's press arm can park ~30s on SPA pages waiting out
// its virtual-URL sync — not worth blocking the caller for.
func (c *Client) HumanClick(x, y float64) error {
	if err := c.HumanMoveTo(x, y); err != nil {
		return err
	}
	// settle: humans stop moving ~40-120ms before pressing
	time.Sleep(time.Duration(40+humanRng.Intn(80)) * time.Millisecond)
	pressMs := 50 + humanRng.Intn(70)
	// Build trajectory to the target and let the engine finish the press
	// inside the same evaluate (mousedown → press gap → mouseup → click).
	from := mousePoint{X: c.mouseX, Y: c.mouseY}
	pts := humanPath(from, mousePoint{X: x, Y: y}, humanRng)
	arr := make([][]float64, len(pts))
	total := 0
	for i := range pts {
		d := 8 + humanRng.Intn(15)
		total += d
		arr[i] = []float64{pts[i].X, pts[i].Y, float64(d)}
	}
	done := make(chan error, 1)
	go func() {
		_, sendErr := c.Send("Input.humanGesture", map[string]interface{}{
			"points": arr, "press": true, "pressDelayMs": pressMs, "x": x, "y": y,
		})
		done <- sendErr
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// challenge-heavy pages keep V8 busy for tens of seconds; the
		// gesture is already queued page-side and will complete there
	}
	c.mouseX, c.mouseY = x, y
	time.Sleep(time.Duration(total+pressMs+140) * time.Millisecond)
	return nil
}

// ElementCenter resolves a selector's nth match to viewport coordinates,
// scrolling it into view first. Returns view-relative center ±30% jitter
// within the box (never a dead-center click).
func (c *Client) ElementCenter(selector string, nth int) (float64, float64, error) {
	// obscura's scrollIntoView can zero the rect of an already-visible
	// element (scroll layout invalidation), so measure first and only
	// scroll when the element sits outside the viewport.
	js := fmt.Sprintf(`(function(){
  var els = document.querySelectorAll('%s');
  if (!els.length) return JSON.stringify({ok:false});
  var el = els[Math.min(%d, els.length-1)];
  function rect() { var r = el.getBoundingClientRect(); return [r.x, r.y, r.width, r.height]; }
  var rr = rect();
  if (rr[2] === 0 && rr[3] === 0) {
    el.scrollIntoView({block:'center', behavior:'instant'});
    rr = rect();
  }
  if (rr[2] === 0 && rr[3] === 0) {
    rr = [el.offsetLeft || 0, el.offsetTop || 0, el.offsetWidth || 1, el.offsetHeight || 1];
  }
  return JSON.stringify({ok:true, x:rr[0], y:rr[1], w:rr[2], h:rr[3]});
})()`, selector, nth)
	raw, err := c.Evaluate(js)
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		OK bool    `json:"ok"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
		W  float64 `json:"w"`
		H  float64 `json:"h"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || !out.OK {
		return 0, 0, fmt.Errorf("selector not found: %s", selector)
	}
	// random point in the inner 60% of the box
	fx := 0.2 + humanRng.Float64()*0.6
	fy := 0.2 + humanRng.Float64()*0.6
	return out.X + out.W*fx, out.Y + out.H*fy, nil
}

// HumanClickSelector: full humanized click on a CSS selector.
func (c *Client) HumanClickSelector(selector string) error {
	x, y, err := c.ElementCenter(selector, 0)
	if err != nil {
		return err
	}
	return c.HumanClick(x, y)
}
