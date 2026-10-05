"""Prism Gateway console UI probe.

Renders every view/sub-tab of the console at pinned viewports against the
isolated mock preview server, then measures WCAG contrast, overflow, keyboard
reachability and semantics from computed styles. No real accounts involved.

Usage (repo root, against the isolated mock preview from
``WB2A_BROWSER_PREVIEW=1 go -C console test -run '^TestAdminBrowserPreview$' -v -timeout 20m``):

    python3 scripts/ui_probe.py --out .build/ui-probe --tag before
"""
import argparse
import json
import sys
from pathlib import Path

# --- views to walk: (screenshot slug, nav view, sub-tab id or None, expected marker) ---
VIEWS = [
    ("01-overview", "overview", None, "通道总览"),
    ("02-workbuddy-accounts", "workbuddy", "workbuddy-view-accounts", "已连接账号"),
    ("03-workbuddy-tasks", "workbuddy", "workbuddy-view-tasks", "自动任务"),
    ("04-workbuddy-models", "workbuddy", "workbuddy-view-models", "模型列表"),
    ("05-workbuddy-chat", "workbuddy", "workbuddy-view-chat", "对话测试"),
    ("06-zcode", "zcode", None, "Zcode 通道"),
    ("07-qoder", "qoder", None, "Qoder 通道"),
    ("08-opencode", "opencode", None, "OpenCode 通道"),
    ("09-usage", "usage", None, "调用统计"),
    ("10-routes", "routes", None, "我的别名表"),
    ("11-access", "access", None, "接入你喜欢的客户端"),
]

VIEWPORTS = {
    "desktop": {"width": 1440, "height": 900},
    "laptop": {"width": 1280, "height": 800},
    "phone": {"width": 390, "height": 844},
}

# ---------------------------------------------------------------- JS payloads

# The measuring kernel, installed once per document. Every payload and the
# self-test go through window.__prism.measure, so the self-test proves the SAME
# code path that produces the reported numbers. A private copy of the maths in
# the self-test stays green after the real classifier breaks.
JS_KERNEL = r"""
() => {
  const chan = (c) => { c/=255; return c<=0.04045 ? c/12.92 : Math.pow((c+0.055)/1.055,2.4); };
  const lum = (r,g,b) => 0.2126*chan(r)+0.7152*chan(g)+0.0722*chan(b);
  const parse = (s) => {
    const t = String(s).trim();
    const m = t.match(/rgba?\(([^)]+)\)/);
    if (m) {
      const p = m[1].split(/[ ,\/]+/).filter(Boolean).map(x=>parseFloat(x));
      if (p.length < 3 || p.slice(0,3).some(Number.isNaN)) return null;
      return {r:p[0], g:p[1], b:p[2], a: p.length>3 ? p[3] : 1};
    }
    const h = t.match(/^#([0-9a-fA-F]{3,8})$/);
    if (h) {
      let v = h[1];
      if (v.length === 3 || v.length === 4) v = v.split('').map(c=>c+c).join('');
      const n = parseInt(v.slice(0,8), 16);
      return {r:(n>>16)&255, g:(n>>8)&255, b:n&255, a: v.length>=8 ? ((n&255)/255) : 1};
    }
    return null;
  };
  const ratio = (fg, bg) => {
    const L1 = lum(fg.r,fg.g,fg.b), L2 = lum(bg.r,bg.g,bg.b);
    const [hi,lo] = L1>L2 ? [L1,L2] : [L2,L1];
    return (hi+0.05)/(lo+0.05);
  };
  const over = (fg, base) => {
    const a = fg.a;
    return {r: fg.r*a + base.r*(1-a), g: fg.g*a + base.g*(1-a), b: fg.b*a + base.b*(1-a), a: 1};
  };
  const css = (c) => `rgb(${Math.round(c.r)},${Math.round(c.g)},${Math.round(c.b)})`;
  // A `background: linear-gradient(...)` shorthand resets background-color to
  // transparent, so a solid-colour walk alone scores light text on a dark
  // gradient against the page canvas. Collect the stops and score the WORST one:
  // conservative, and it can never invent a pass.
  const stopsOf = (el) => {
    const bi = getComputedStyle(el).backgroundImage;
    if (!bi || bi === 'none') return {stops: [], washes: false};
    const re = /(rgba?\([^)]+\)|#[0-9a-fA-F]{3,8})/g;
    const all = [];
    let m;
    while ((m = re.exec(bi)) !== null) {
      const c = parse(m[1]);
      if (c) all.push(c);
    }
    const kept = all.filter(c => c.a > 0.05);
    // A gradient that carries a transparent stop is a partial wash: the bare
    // layers beneath it are a real backdrop for text further down the page.
    return {stops: kept, washes: kept.length < all.length};
  };
  const resolveBg = (el) => {
    let stack = [], node = el, sawGradient = false;
    while (node && node.nodeType === 1) {
      const cs = getComputedStyle(node);
      const {stops, washes} = stopsOf(node);
      if (stops.length) sawGradient = true;
      const solid = parse(cs.backgroundColor);
      if (stops.length && stops.every(s => s.a >= 1)) {
        // Paint order: gradient stops sit on top of this node's background-color,
        // which sits on top of everything collected from descendants.
        let beneath = {r:255,g:255,b:255,a:1};
        for (let i = stack.length-1; i >= 0; i--) beneath = over(stack[i], beneath);
        if (solid && solid.a > 0) beneath = over(solid, beneath);
        const candidates = stops.map(s => over(s, beneath));
        if (washes) candidates.push(beneath);
        return {candidates, sawGradient: true};
      }
      if (solid && solid.a > 0) {
        stack.push(solid);
        if (solid.a >= 1) break;
      }
      node = node.parentElement;
    }
    let base = {r:255,g:255,b:255,a:1};
    for (let i = stack.length-1; i >= 0; i--) base = over(stack[i], base);
    return {candidates: [base], sawGradient};
  };
  const measure = (el) => {
    const cs = getComputedStyle(el);
    const fg = parse(cs.color);
    if (!fg) return null;
    const res = resolveBg(el);
    if (!res.candidates.length) return null;
    const sizePx = parseFloat(cs.fontSize) || 0;
    const weight = parseInt(cs.fontWeight, 10) || 400;
    // WCAG large-text exemption: >=24px, or >=18.66px at weight >=700.
    const need = (sizePx >= 24 || (sizePx >= 18.66 && weight >= 700)) ? 3.0 : 4.5;
    let worst = null, wbg = null;
    for (const bg of res.candidates) {
      const r = ratio(over(fg, bg), bg);
      if (worst === null || r < worst) { worst = r; wbg = bg; }
    }
    return {ratio: worst, need, sizePx, weight, onGradient: !!res.sawGradient,
            stops: res.candidates.length, fgCss: css(over(fg, wbg)), bgCss: css(wbg)};
  };
  // The focus indicator's own visibility, scored the same way for a live tab stop
  // and for the self-test fixtures: one code path, so the guard cannot drift from
  // the thing it guards.
  const ringScore = (el) => {
    const cs = getComputedStyle(el);
    const colors = [];
    // outlineStyle 'auto' is the UA ring (Chrome draws a high-contrast double
    // ring); it has no parseable colour and must not be scored as a weak custom ring.
    const ua = cs.outlineStyle === 'auto' && parseFloat(cs.outlineWidth) > 0;
    if (!ua && cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0) colors.push(cs.outlineColor);
    if (cs.boxShadow && cs.boxShadow !== 'none') {
      (cs.boxShadow.match(/rgba?\([^)]+\)|#[0-9a-fA-F]{3,8}/g) || []).forEach(c => colors.push(c));
    }
    if (!colors.length) return {ring: null, uaRing: ua};
    // Only the OUTLINE and the FIRST (topmost) box-shadow layer can be the focus
    // indicator; scoring decorative lower layers too would let a wide soft shadow
    // masquerade as a visible ring.
    // A positive outline-offset draws the ring outside the border box, so it never
    // touches the element's own fill: both of its edges sit on the backdrop of the
    // container. Scoring an offset ring against the element's own background
    // invents a collision nobody can see (a dark ring beside an indigo button reads
    // as a clean two-tone outline), so the backdrop resolves from the parent.
    // With offset 0 the ring does abut its own fill, and that stays scored.
    const offset = parseFloat(cs.outlineOffset) || 0;
    const bg = resolveBg(offset > 0 ? (el.parentElement || el) : el).candidates;
    let best = null;
    for (const raw of colors.slice(0, 2)) {
      const c = parse(raw);
      if (!c || c.a <= 0.02) continue;
      for (const b of bg) {
        const eff = c.a >= 1 ? c : over(c, b);
        const r = ratio(eff, b);
        if (Number.isFinite(r) && (best === null || r > best)) best = r;
      }
    }
    return {ring: best === null ? null : Math.round(best * 100) / 100, uaRing: ua};
  };
  window.__prism = {measure, resolveBg, parse, ratio, over, stopsOf, lum, ringScore};
  return {installed: typeof window.__prism.measure === 'function'};
}
"""

JS_CONTRAST = r"""
() => {
  if (!window.__prism) throw new Error('kernel not installed');
  const {measure} = window.__prism;
  const hasOwnText = (el) => Array.from(el.childNodes).some(n =>
    n.nodeType === 3 && n.textContent.trim().length > 0);
  const path = (el) => {
    const parts = [];
    let node = el;
    for (let i = 0; i < 4 && node && node.nodeType === 1; i++) {
      let s = node.tagName.toLowerCase();
      if (node.id) { s += '#' + node.id; parts.unshift(s); break; }
      if (node.className && typeof node.className === 'string') {
        const c = node.className.trim().split(/\s+/).filter(Boolean).slice(0,2).join('.');
        if (c) s += '.' + c;
      }
      parts.unshift(s);
      node = node.parentElement;
    }
    return parts.join(' > ');
  };
  const visible = (el) => {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || cs.opacity === '0') return false;
    const r = el.getBoundingClientRect();
    // 2px floor: screen-reader-only slabs carry text but render no glyphs, so a
    // ratio computed for them is not something a user can fail to read.
    return r.width > 2 && r.height > 2;
  };
  const out = [], seen = new Set();
  let skipped = 0;
  document.querySelectorAll('body *').forEach(el => {
    if (!hasOwnText(el) || !visible(el)) return;
    // WCAG SC 1.4.3 exempts inactive controls, and the reduced-alpha disabled
    // state here is deliberate. Count the exemption instead of hiding it.
    if (el.disabled || el.closest(':disabled,[aria-disabled="true"]')) { skipped++; return; }
    const m = measure(el);
    if (!m) return;
    const text = el.textContent.trim().slice(0, 40);
    const key = path(el) + '|' + text;
    if (seen.has(key)) return;
    seen.add(key);
    out.push({
      el: path(el), text, sizePx: m.sizePx, weight: m.weight,
      ratio: Math.round(m.ratio*100)/100, need: m.need, pass: m.ratio >= m.need,
      fg: m.fgCss, bg: m.bgCss, onGradient: m.onGradient, stops: m.stops
    });
  });
  window.__prismInactive = skipped;
  return out;
}
"""

# Each row declares an expected verdict and what it discriminates, so a kernel
# that collapsed the gradient branch (or blindly trusted it) goes red, not green.
SELFTEST_SAMPLES = [
    # label, fg, backdrop css, expect pass?, extra floor
    ("faint_on_solid", "#9aa0bb", "background:#f8f9fe", False, None),
    ("ink_on_solid", "#171a2b", "background:#f8f9fe", True, None),
    # Gradient branch dead => falls through to page canvas #f3f4fb => ~1.07 => FAIL.
    ("white_on_dark_gradient", "#ffffff", "background:linear-gradient(135deg,#0e1020,#0a0c18)", True, 8.0),
    # Gradient wrongly treated as an opaque white slab => faint text wrongly PASSes.
    ("faint_on_light_gradient", "#9aa0bb", "background:linear-gradient(135deg,#f8f9fe,#ffffff)", False, None),
    # Must score the WORST stop; using only stops[0] would report ~15 => wrongly PASS.
    ("worst_stop_across_gradient", "#ffffff", "background:linear-gradient(90deg,#0e1020,#ffffff)", False, None),
    # A fully transparent gradient overlay must not replace the solid backdrop beneath it.
    ("ink_under_translucent_overlay", "#171a2b",
     "background-color:#ffffff;background-image:linear-gradient(rgba(255,255,255,0),rgba(255,255,255,0))", True, 8.0),
    # A fading wash must ALSO score the bare canvas it dissolves into. Scoring only
    # the dark stop would report 18.86 => wrongly PASS; the real page has this shape.
    ("white_on_fading_wash", "#ffffff",
     "background:radial-gradient(1200px 620px at 10% -12%,#0e1020 0%,rgba(0,0,0,0) 58%),#ffffff", False, None),
]

# The focus-ring path gets its own fixtures for the same reason: the two rows
# below are a matched pair that only the outline-offset geometry separates, so a
# change to the backdrop rule moves one and not the other.
RING_SAMPLES = [
    # label, parent bg css, button css, min, max, ua-expected
    ("offset_ring_uses_container_backdrop",
     "background:#ffffff",
     "background:#5b4bf0;color:#fff;outline:2px solid #3a2bc4;outline-offset:2px",
     6.0, None, False),
    # Same colours, offset 0: the ring now truly abuts its own indigo fill, so the
    # rule must still report the collision. If this passes too, the check is dead.
    ("zero_offset_ring_against_own_fill",
     "background:#ffffff",
     "background:#5b4bf0;color:#fff;outline:2px solid #3a2bc4;outline-offset:0px",
     None, 3.0, False),
    # A 12%-alpha shadow ring is present but nearly invisible on white.
    ("translucent_shadow_ring",
     "background:#ffffff",
     "background:#ffffff;box-shadow:0 0 0 3px rgba(58,43,196,0.12)",
     None, 3.0, False),
    # The UA double ring has no parseable colour; scoring it as absent would flag
    # every control that relies on it.
    ("ua_auto_ring",
     "background:#ffffff",
     "background:#5b4bf0;color:#fff;outline:auto 2px",
     None, None, True),
]

JS_RING_SELFTEST = r"""
(samples) => {
  if (!window.__prism) throw new Error('kernel not installed');
  const {ringScore} = window.__prism;
  return samples.map((s) => {
    const host = document.createElement('div');
    host.style.cssText = 'position:fixed;left:-9999px;top:0;width:320px;height:80px;' + s.parentCss;
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.style.cssText = 'width:160px;height:40px;' + s.css;
    btn.textContent = 'RING';
    host.append(btn);
    document.body.append(host);
    const got = ringScore(btn);
    host.remove();
    const reasons = [];
    if (s.wantUa && !got.uaRing) reasons.push('expected UA auto ring, got none');
    if (!s.wantUa && got.uaRing) reasons.push('unexpected UA ring');
    if (s.min !== null && s.min !== undefined && !(got.ring >= s.min)) {
      reasons.push('ring=' + got.ring + ' expected >= ' + s.min);
    }
    if (s.max !== null && s.max !== undefined && !(got.ring < s.max)) {
      reasons.push('ring=' + got.ring + ' expected < ' + s.max);
    }
    return {label: s.label, ring: got.ring, uaRing: got.uaRing, ok: reasons.length === 0,
            want: (s.wantUa ? 'ua-ring' : (s.max != null ? '<' + s.max : '>=' + s.min)),
            ratio: got.ring, bg: '', stops: '', floor: null,
            why: reasons.join('; ')};
  });
}
"""

JS_SELFTEST = r"""
(samples) => {
  if (!window.__prism) throw new Error('kernel not installed');
  const {measure} = window.__prism;
  const res = [];
  for (const s of samples) {
    const wrap = document.createElement('div');
    wrap.style.cssText = 'position:fixed;left:-9999px;top:0;width:320px;height:40px;' + s.bgCss;
    const span = document.createElement('span');
    span.textContent = 'SELFTEST';
    span.style.cssText = 'color:' + s.fg + ';font-size:12px;font-weight:400;';
    wrap.append(span);
    document.body.append(wrap);
    const m = measure(span);
    const verdict = m ? (m.ratio >= m.need) : null;
    const ok = !!m && verdict === s.wantPass
      && (s.floor == null || m.ratio >= s.floor);
    res.push({label: s.label, want: s.wantPass ? 'pass' : 'fail', ok,
              floor: s.floor == null ? null : s.floor,
              ratio: m ? Math.round(m.ratio*100)/100 : null,
              bg: m ? m.bgCss : null, stops: m ? m.stops : null});
    wrap.remove();
  }
  return {samples: res, live: res.every(r => r.ok)};
}
"""

JS_OVERFLOW = r"""
() => {
  const doc = document.documentElement;
  const wide = [];
  const shown = (el, cs) => {
    const r = el.getBoundingClientRect();
    if (r.width <= 2 || r.height <= 2) return false;          // .sr-only style 1px slabs
    if (cs.clipPath && cs.clipPath !== 'none') return false;
    if (cs.clip && cs.clip !== 'auto') return false;
    return true;
  };
  document.querySelectorAll('body *').forEach(el => {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return;
    if (!shown(el, cs)) return;
    if (el.scrollWidth > el.clientWidth + 2 && cs.overflowX !== 'auto' && cs.overflowX !== 'scroll') {
      const r = el.getBoundingClientRect();
      if (r.width > 0) wide.push({
        el: el.tagName.toLowerCase() + (el.id ? '#'+el.id : '') +
            (el.className && typeof el.className === 'string' ? '.'+el.className.trim().split(/\s+/)[0] : ''),
        scrollWidth: el.scrollWidth, clientWidth: el.clientWidth});
    }
  });
  return {
    documentOverflowX: doc.scrollWidth > doc.clientWidth,
    docScrollWidth: doc.scrollWidth, docClientWidth: doc.clientWidth,
    clipped: wide.slice(0, 12)
  };
}
"""

JS_ACTIVE = r"""
() => {
  const el = document.activeElement;
  if (!el || el === document.body) return null;
  const cs = getComputedStyle(el);
  // A focused element inside an unrevealed container (a collapsed <details>, a
  // hidden pane) is not something a keyboard user can see, so it is not a stop.
  if (el.closest('[hidden], [aria-hidden="true"]')) return {skip: true};
  const r = el.getBoundingClientRect();
  const cls = (typeof el.className === 'string' && el.className.trim())
    ? '.' + el.className.trim().split(/\s+/)[0] : '';
  const P = window.__prism;
  // A ring can exist and still be invisible: WCAG 2.2 focus-appearance wants the
  // indicator to reach 3:1 against the surrounding colour. `box-shadow: 0 0 0 3px
  // #6d5efc1f` is 12% alpha, i.e. ~1.03:1 on white, which a "ring present" check
  // would wave through.
  const scored = P ? P.ringScore(el) : {ring: null, uaRing: false};
  const {ring, uaRing} = scored;
  const anyRing = uaRing || ring !== null;
  return {
    el: el.tagName.toLowerCase() + (el.id ? '#'+el.id : '') + cls,
    text: (el.innerText || el.value || el.getAttribute('aria-label') || '').trim().slice(0, 24),
    // Chrome scrolls a newly focused element to the top of the viewport with
    // device-pixel rounding, which lands a fully visible button at top=-0.00003.
    // A 1px slack on each edge keeps that from reading as "focus is off screen";
    // a genuinely clipped stop is off by tens of px and still reports.
    inViewport: r.top >= -1 && r.bottom <= innerHeight + 1 && r.left >= -1,
    outline: (cs.outlineStyle + ' ' + cs.outlineWidth).trim(),
    boxShadow: cs.boxShadow.slice(0, 40),
    ring, uaRing, weakRing: !uaRing && ring !== null && ring < 3.0,
    noRing: !anyRing,
  };
}
"""

JS_SEMANTICS = r"""
() => {
  const vis = (el) => {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  const pick = (sel) => Array.from(document.querySelectorAll(sel)).filter(vis);
  // A live region counts when it is rendered, not when it has content: an empty
  // role="alert" has a zero-height box yet is announced the moment text lands in
  // it. Requiring a bounding box here would demand a permanent empty gap in the
  // layout, which is worse design, so the predicate is deliberately size-free.
  // checkVisibility still walks ancestors, so a region inside a display:none
  // subtree (the console shell while the login view is up) stays excluded.
  const rendered = (el) => el.checkVisibility({visibilityProperty: true});
  // An input's accessible name comes from its <label for>, not from its value:
  // counting a correctly labelled password field as "unnamed" would bury the
  // real finding (which controls have NO name at all).
  const named = (el) => {
    if (el.getAttribute('aria-label') || el.getAttribute('title') || el.getAttribute('alt')) return true;
    if (el.getAttribute('aria-labelledby')) return true;
    if (el.id && document.querySelector('label[for="' + CSS.escape(el.id) + '"]')) return true;
    if (el.closest('label')) return true;
    return false;
  };
  const nameOf = (el) => (el.innerText || el.value || el.getAttribute('aria-label')
    || el.getAttribute('title') || '').trim();
  const controls = pick('button, input, select, textarea, a[href]');
  const unnamed = controls.filter(el => !nameOf(el) && !named(el));
  const inputs = pick('input, select, textarea');
  const unlabelled = inputs.filter(el => {
    if (el.type === 'hidden' || el.disabled) return false;
    return !named(el);
  });
  const tables = pick('table');
  const focusables = pick('a[href], button, input:not([type=hidden]), select, textarea, [tabindex]:not([tabindex="-1"])');
  return {
    landmarks: {header: pick('header').length, nav: pick('nav').length,
                main: pick('main').length, aside: pick('aside').length},
    h1: pick('h1').length, h2: pick('h2').length, h3: pick('h3').length,
    h1Texts: pick('h1').map(el => el.innerText.trim().slice(0, 30)),
    controls: controls.length, focusables: focusables.length,
    unnamedControls: unnamed.slice(0, 8).map(el => el.tagName.toLowerCase() + (el.id ? '#'+el.id : '')),
    unlabelledInputs: unlabelled.slice(0, 8).map(el => el.tagName.toLowerCase() + (el.id ? '#'+el.id : '')),
    titleOnly: pick('[title]').length,
    tables: tables.length,
    tablesWithoutHeader: tables.filter(t => !t.querySelector('th')).length,
    ariaAttrs: document.querySelectorAll('[aria-label],[aria-live],[role],[aria-busy],[aria-current]').length,
    dialogs: document.querySelectorAll('dialog, [popover]').length,
    divButtons: pick('[onclick],[role=button]').length,
    // A live region only exists for assistive tech if it is in the a11y tree
    // right now: role=status/alert/log carry an implicit aria-live, and a
    // display:none region is not announced. Count both shapes, require presence.
    liveRegions: Array.from(document.querySelectorAll(
      '[aria-live],[role="status"],[role="alert"],[role="log"]')).filter(rendered).length,
  };
}
"""

JS_STATE = r"""
() => {
  const vis = (id) => { const el = document.getElementById(id); if (!el) return null;
    return !el.hidden && getComputedStyle(el).display !== 'none'; };
  return {
    breadcrumb: (document.getElementById('breadcrumb-name')||{}).textContent || '',
    visiblePages: Array.from(document.querySelectorAll('[data-page]')).filter(p=>!p.hidden).map(p=>p.dataset.page),
    serviceBadge: (document.getElementById('service-state')||{}).textContent || '',
    notice: vis('notice'),
    accountsEmpty: vis('accounts-empty'),
    bodyTextLength: document.body.innerText.length
  };
}
"""

# ------------------------------------------------------------------- driver


def install(page):
    """window.__prism lives on the document; reinstall after every navigation."""
    got = page.evaluate(JS_KERNEL)
    if not got.get("installed"):
        raise RuntimeError(f"contrast kernel did not install: {got}")


def login(page, base, key):
    page.goto(base + "/", wait_until="domcontentloaded")
    page.wait_for_selector("#login-view:not([hidden])", timeout=15000)
    install(page)
    page.fill("#admin-key", key)
    page.click("#login-form button[type=submit]")
    page.wait_for_selector("#console-view:not([hidden])", timeout=15000)
    install(page)
    page.wait_for_timeout(1500)


def settle(page):
    """The console keeps a visible-page poll in flight, so networkidle never fires.
    Wait for the DOM to stop changing instead."""
    try:
        page.wait_for_function(
            """() => { const t = window.__probeMark;
                         window.__probeMark = document.body.innerText.length;
                         return t === window.__probeMark; }""",
            timeout=6000)
    except Exception:
        pass
    page.wait_for_timeout(600)


def run_selftest(page):
    payload = [{"label": l, "fg": f, "bgCss": b, "wantPass": w, "floor": fl}
               for (l, f, b, w, fl) in SELFTEST_SAMPLES]
    contrast = page.evaluate(JS_SELFTEST, payload)
    ring_payload = [{"label": l, "parentCss": pc, "css": c, "min": lo, "max": hi, "wantUa": ua}
                    for (l, pc, c, lo, hi, ua) in RING_SAMPLES]
    ring = page.evaluate(JS_RING_SELFTEST, ring_payload)
    # Both kernels gate the run: a ring classifier that cannot tell an offset ring
    # from an abutting one would report every focus stop as clean.
    return {"samples": contrast["samples"] + ring,
            "live": contrast["live"] and all(r["ok"] for r in ring)}


def tab_walk(page, limit=45):
    """Real Tab presses. A synthetic keydown never moves focus, so a JS-simulated
    walk would report an order that does not exist."""
    stops, first_key, seen = [], None, set()
    for _ in range(limit):
        page.keyboard.press("Tab")
        cur = page.evaluate(JS_ACTIVE)
        if not cur:
            break
        if cur.get("skip"):
            continue
        key = cur["el"] + "|" + cur["text"]
        if first_key is None:
            first_key = key
        elif key == first_key or key in seen:
            break
        seen.add(key)
        stops.append(cur)
    return {
        "stops": len(stops),
        "noRing": [s["el"] for s in stops if s.get("noRing")][:10],
        "weakRing": [{"el": s["el"], "ring": s.get("ring")} for s in stops
                     if s.get("weakRing")][:10],
        "offscreen": [s["el"] for s in stops if not s["inViewport"]][:10],
        "minRing": min([s["ring"] for s in stops if s.get("ring") is not None], default=None),
        "detail": stops[:16],
    }


def walk(pw, base, key, out_dir, tag, vp_name):
    vp = VIEWPORTS[vp_name]
    browser = pw.chromium.launch(channel="msedge")
    ctx = browser.new_context(viewport=vp, device_scale_factor=1, locale="zh-CN")
    page = ctx.new_page()
    shots, reports = [], {}

    # login page first (before auth clears it)
    page.goto(base + "/", wait_until="domcontentloaded")
    page.wait_for_selector("#login-view:not([hidden])", timeout=15000)
    install(page)
    page.wait_for_timeout(600)
    selftest = run_selftest(page)
    if not selftest.get("live"):
        print("SELFTEST FAILED (contrast classifier is not trustworthy):",
              json.dumps(selftest, ensure_ascii=False))
        print("aborting: the same measure() path must call a known-fail a fail and a "
              "known-pass a pass, or the reported numbers mean nothing")
        browser.close()
        sys.exit(3)
    shot = out_dir / f"{tag}-{vp_name}-00-login.png"
    page.screenshot(path=str(shot), full_page=(vp_name == "phone"))
    shots.append((shot.name, "login", 0))
    reports[f"{tag}-{vp_name}-00-login"] = {
        "contrast": page.evaluate(JS_CONTRAST),
        "contrastInactive": page.evaluate("() => window.__prismInactive || 0"),
        "overflow": page.evaluate(JS_OVERFLOW),
        "semantics": page.evaluate(JS_SEMANTICS),
        "tabOrder": tab_walk(page) if vp_name == "desktop" else {},
    }

    login(page, base, key)

    for slug, view, tab, marker in VIEWS:
        name = f"{tag}-{vp_name}-{slug}"
        try:
            page.click(f'button[data-view="{view}"]')
            settle(page)
            if tab:
                page.click(f"#{tab}")
                settle(page)
            state = page.evaluate(JS_STATE)
            shot = out_dir / f"{name}.png"
            page.screenshot(path=str(shot), full_page=(vp_name == "phone"))
            shots.append((shot.name, marker, state["bodyTextLength"]))
            rep = {
                "state": state,
                "contrast": page.evaluate(JS_CONTRAST),
                "contrastInactive": page.evaluate("() => window.__prismInactive || 0"),
                "overflow": page.evaluate(JS_OVERFLOW),
                "semantics": page.evaluate(JS_SEMANTICS),
            }
            if vp_name == "desktop":
                rep["tabOrder"] = tab_walk(page)
            reports[name] = rep
        except Exception as exc:  # keep walking; a dead view is itself a finding
            reports[name] = {"error": f"{type(exc).__name__}: {exc}"}

    ctx.close()
    browser.close()
    return shots, reports, selftest


def summarise(reports):
    lines = []
    for name, rep in sorted(reports.items()):
        if "error" in rep:
            lines.append(f"{name}: ERROR {rep['error']}")
            continue
        con = rep.get("contrast") or []
        fails = [c for c in con if not c["pass"]]
        ovf = rep.get("overflow") or {}
        sem = rep.get("semantics") or {}
        tab = rep.get("tabOrder") or {}
        lines.append(
            f"{name}: text={len(con)} fail={len(fails)} inactive_skipped={rep.get('contrastInactive')} "
            f"worst={min([f['ratio'] for f in fails], default='—')} "
            f"overflowX={ovf.get('documentOverflowX')} clipped={len(ovf.get('clipped', []))} "
            f"h1={sem.get('h1')} unnamed={len(sem.get('unnamedControls', []))} "
            f"unlabelled={len(sem.get('unlabelledInputs', []))} aria={sem.get('ariaAttrs')} "
            f"tabs={tab.get('stops','-')} weakRing={len(tab.get('weakRing', []))} "
            f"noRing={len(tab.get('noRing', []))} offscreen={len(tab.get('offscreen', []))} "
            f"minRing={tab.get('minRing')}"
        )
    return lines


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:17864")
    ap.add_argument("--key", default="browser-preview-key-mock-only-12345")
    ap.add_argument("--out", default=".build/ui-probe")
    ap.add_argument("--tag", default="before")
    ap.add_argument("--viewports", default="desktop,phone")
    args = ap.parse_args()

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    from playwright.sync_api import sync_playwright

    all_reports, all_selftests = {}, {}
    prefix = args.tag + "-"
    with sync_playwright() as pw:
        for vp in [v.strip() for v in args.viewports.split(",") if v.strip()]:
            shots, reports, selftest = walk(pw, args.base, args.key, out_dir, args.tag, vp)
            all_reports.update(reports)
            all_selftests[vp] = selftest
            print(f"\n=== {vp} · self-test through the real measure()/ringScore() ===")
            for s in selftest["samples"]:
                print(f"  {'ok  ' if s['ok'] else 'DEAD'} {s['label']}: want={s['want']} "
                      f"got={s['ratio']} bg={s['bg']} stops={s['stops']}"
                      + (f" floor>={s['floor']}" if s["floor"] else "")
                      + ("" if s["ok"] else "  <- " + s.get("why", "")))
            for line in summarise(reports):
                print("  " + line)
            print("  screenshots:", ", ".join(s[0] for s in shots))

    (out_dir / f"{args.tag}-probe.json").write_text(
        json.dumps({"selftest": all_selftests, "reports": all_reports},
                   ensure_ascii=False, indent=1), encoding="utf-8")

    print("\n=== CONTRAST FAILURES (all views, worst first) ===")
    agg = {}
    for name, rep in all_reports.items():
        for f in (rep.get("contrast") or []):
            if f["pass"]:
                continue
            k = (f["el"], f["fg"], f["bg"], f["sizePx"], f["weight"], f["need"])
            cur = agg.setdefault(k, {"ratio": f["ratio"], "views": [], "text": f["text"]})
            cur["views"].append(name[len(prefix):] if name.startswith(prefix) else name)
            cur["ratio"] = min(cur["ratio"], f["ratio"])
    for k, v in sorted(agg.items(), key=lambda kv: kv[1]["ratio"]):
        el, fg, bg, size, weight, need = k
        print(f"  {v['ratio']:>5.2f} (need {need})  {size}px/{weight}  {fg} on {bg}  "
              f"{el}  [{len(v['views'])} views]  «{v['text'][:26]}»")

    print("\n=== SEMANTICS GAPS ===")
    for name, rep in sorted(all_reports.items()):
        sem = rep.get("semantics") or {}
        if not sem:
            continue
        gaps = []
        if sem.get("h1", 0) == 0:
            gaps.append("no-visible-h1")
        if sem.get("unnamedControls"):
            gaps.append("unnamed:" + ",".join(sem["unnamedControls"]))
        if sem.get("unlabelledInputs"):
            gaps.append("unlabelled:" + ",".join(sem["unlabelledInputs"]))
        if sem.get("tablesWithoutHeader"):
            gaps.append(f"table-no-th:{sem['tablesWithoutHeader']}")
        if sem.get("divButtons"):
            gaps.append(f"div-onclick:{sem['divButtons']}")
        if not sem.get("liveRegions"):
            gaps.append("no-live-region")
        if (rep.get("tabOrder") or {}).get("noRing"):
            gaps.append("no-focus-ring:" + ",".join(rep["tabOrder"]["noRing"]))
        if gaps:
            print(f"  {name[len(prefix):] if name.startswith(prefix) else name}: " + " | ".join(gaps))

    print("\n=== KEYBOARD WALK (desktop) ===")
    for name, rep in sorted(all_reports.items()):
        tab = rep.get("tabOrder") or {}
        if not tab:
            continue
        short = name[len(prefix):] if name.startswith(prefix) else name
        print(f"  {short}: stops={tab.get('stops')} minFocusRing={tab.get('minRing')} "
              f"weakRing={len(tab.get('weakRing', []))} noRing={tab.get('noRing')} "
              f"offscreen={tab.get('offscreen')}")

    print("\n=== OVERFLOW ===")
    for name, rep in sorted(all_reports.items()):
        ovf = rep.get("overflow") or {}
        if ovf.get("documentOverflowX") or ovf.get("clipped"):
            short = name[len(prefix):] if name.startswith(prefix) else name
            print(f"  {short}: docOverflowX={ovf.get('documentOverflowX')} "
                  f"({ovf.get('docScrollWidth')}>{ovf.get('docClientWidth')}) "
                  + json.dumps(ovf.get("clipped", [])[:4], ensure_ascii=False))

    print("\nwritten:", out_dir / f"{args.tag}-probe.json")


if __name__ == "__main__":
    main()
