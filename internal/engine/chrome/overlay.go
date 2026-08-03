package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// OverlayJS is the injected OOB overlay script (issue B-44). The overlay
// lives in a closed Shadow DOM attached to documentElement, is styled with
// position:fixed and an extreme z-index, and re-attaches itself whenever the
// page removes it — page CSS/JS cannot hide or remove it. Buttons write the
// human's decision to a global slot the driver polls.
const OverlayJS = `(function(){
  const SLOT = '__symbrowse_oob_result__';
  const ID = %q;
  const TITLE = %q;
  const REASON = %q;
  const COUNTDOWN = %d;
  window[SLOT] = 'pending';
  function makeHost() {
    const host = document.createElement('div');
    host.id = 'symbrowse-oob-host';
    host.style.all = 'initial';
    host.style.position = 'fixed';
    host.style.top = '0';
    host.style.left = '0';
    host.style.right = '0';
    host.style.zIndex = '2147483647';
    host.style.fontFamily = '-apple-system, system-ui, sans-serif';
    const shadow = host.attachShadow({mode: 'closed'});
    const style = document.createElement('style');
    style.textContent = [
      ':host{all:initial}',
      '.sb-oob{border:2px solid #b45309;background:#fffbeb;color:#1c1917;padding:14px 18px;',
      'box-shadow:0 4px 24px rgba(0,0,0,.35);display:flex;align-items:center;gap:16px;flex-wrap:wrap}',
      '.sb-oob strong{font-size:14px}',
      '.sb-oob span{font-size:13px;opacity:.85}',
      '.sb-oob .timer{font-variant-numeric:tabular-nums;font-weight:600;color:#b45309}',
      '.sb-oob button{border:1px solid #1c1917;background:#fff;padding:6px 14px;font-size:13px;',
      'cursor:pointer;border-radius:6px;font-family:inherit}',
      '.sb-oob button.done{background:#166534;border-color:#166534;color:#fff}',
      '.sb-oob button.abort{background:#fff;color:#991b1b;border-color:#991b1b}'
    ].join('');
    const box = document.createElement('div');
    box.className = 'sb-oob';
    const strong = document.createElement('strong');
    strong.textContent = TITLE;
    const reason = document.createElement('span');
    reason.textContent = REASON;
    const timer = document.createElement('span');
    timer.className = 'timer';
    const done = document.createElement('button');
    done.className = 'done';
    done.textContent = 'Fertig';
    done.addEventListener('click', function(){ window[SLOT] = 'completed'; removeHost(); });
    const abort = document.createElement('button');
    abort.className = 'abort';
    abort.textContent = 'Abbrechen';
    abort.addEventListener('click', function(){ window[SLOT] = 'cancelled'; removeHost(); });
    box.appendChild(strong); box.appendChild(reason); box.appendChild(timer);
    box.appendChild(done); box.appendChild(abort);
    shadow.appendChild(style); shadow.appendChild(box);
    if (COUNTDOWN > 0) {
      let left = COUNTDOWN;
      timer.textContent = left + 's';
      const interval = setInterval(function(){
        left -= 1;
        if (left <= 0) { clearInterval(interval); timer.textContent = ''; }
        else { timer.textContent = left + 's'; }
      }, 1000);
    }
    return host;
  }
  function removeHost() {
    const host = document.getElementById('symbrowse-oob-host');
    if (host && host.parentNode) host.parentNode.removeChild(host);
  }
  let host = makeHost();
  (document.documentElement || document.body).appendChild(host);
  const observer = new MutationObserver(function(mutations){
    for (const mutation of mutations) {
      for (const node of mutation.removedNodes) {
        if (node.id === 'symbrowse-oob-host') {
          host = makeHost();
          (document.documentElement || document.body).appendChild(host);
        }
      }
    }
  });
  observer.observe(document.documentElement, {childList: true, subtree: false});
  return true;
})()`

const overlayResultExpression = `(window.__symbrowse_oob_result__ || 'pending')`

// InstallOverlay injects the OOB overlay through Runtime evaluation.
func (e *Engine) InstallOverlay(ctx context.Context, page engine.Page, request engine.OverlayRequest) error {
	script := fmt.Sprintf(OverlayJS, request.ID, request.Title, request.Reason, request.CountdownSeconds)
	result, err := e.Evaluate(ctx, page, script)
	if err != nil {
		return fmt.Errorf("install oob overlay: %w", err)
	}
	if result.ExceptionText != "" {
		return fmt.Errorf("install oob overlay: %s", result.ExceptionText)
	}
	return nil
}

// RemoveOverlay detaches the OOB overlay.
func (e *Engine) RemoveOverlay(ctx context.Context, page engine.Page) error {
	expression := `(function(){ const h = document.getElementById('symbrowse-oob-host'); if (h && h.parentNode) h.parentNode.removeChild(h); return true; })()`
	result, err := e.Evaluate(ctx, page, expression)
	if err != nil {
		return err
	}
	if result.ExceptionText != "" {
		return errors.New(result.ExceptionText)
	}
	return nil
}

// OverlayResult reads the human's decision from the overlay slot.
func (e *Engine) OverlayResult(ctx context.Context, page engine.Page) (string, error) {
	result, err := e.Evaluate(ctx, page, overlayResultExpression)
	if err != nil {
		return "", err
	}
	if result.ExceptionText != "" {
		return "", errors.New(result.ExceptionText)
	}
	var decision string
	if err := json.Unmarshal(result.Value, &decision); err != nil {
		return "", fmt.Errorf("decode overlay result: %w", err)
	}
	return decision, nil
}

var _ engine.OverlayHost = (*Engine)(nil)
