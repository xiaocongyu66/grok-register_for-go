package obscura

import (
	"fmt"
	"strings"
)

// Direct (non-humanized) keyboard input. Complements the HumanType* family:
//   - HumanTypeInto / HumanTypeAppend — per-key trusted event chain with a
//     lognormal human rhythm (use when the page scores typing behavior)
//   - TypeFull — one-shot set + complete input/change event chain, the
//     semantics of paste/autofill (use when speed matters and the field is
//     framework-managed: React/Preact/Vue trackers stay consistent because
//     the write goes through the prototype accessor)
//   - Type — legacy plain assignment, no events; kept for old callers

// TypeFull sets the whole value at once and fires the full trusted event
// chain (focus → input → change) exactly like a paste or autofill fill.
func (c *Client) TypeFull(selector, text string) error {
	escaped := strings.ReplaceAll(text, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "'", "\\'")
	escaped = strings.ReplaceAll(escaped, "\n", "\\n")
	js := fmt.Sprintf(`(function(){
  var el = document.querySelector('%s');
  if (!el) return 'no-element';
  if (el.focus) el.focus();
  globalThis.__obscura_setFieldValue(el, 'value', '%s');
  el.dispatchEvent(globalThis.__obscura_markTrusted(new Event('input', {bubbles:true})));
  el.dispatchEvent(globalThis.__obscura_markTrusted(new Event('change', {bubbles:true})));
  return 'ok';
})()`, selector, escaped)
	res, err := c.Evaluate(js)
	if err != nil {
		return err
	}
	if res == "no-element" {
		return fmt.Errorf("selector not found: %s", selector)
	}
	return nil
}

// TypeFullExpr is TypeFull for an arbitrary element-returning expression
// (frames, shadow hosts, non-selector lookups).
func (c *Client) TypeFullExpr(focusExpr, text string) error {
	escaped := strings.ReplaceAll(text, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "'", "\\'")
	escaped = strings.ReplaceAll(escaped, "\n", "\\n")
	js := fmt.Sprintf(`(function(){
  var el = (%s);
  if (!el) return 'no-element';
  if (el.focus) el.focus();
  globalThis.__obscura_setFieldValue(el, 'value', '%s');
  el.dispatchEvent(globalThis.__obscura_markTrusted(new Event('input', {bubbles:true})));
  el.dispatchEvent(globalThis.__obscura_markTrusted(new Event('change', {bubbles:true})));
  return 'ok';
})()`, focusExpr, escaped)
	res, err := c.Evaluate(js)
	if err != nil {
		return err
	}
	if res == "no-element" {
		return fmt.Errorf("focus expression matched nothing")
	}
	return nil
}
