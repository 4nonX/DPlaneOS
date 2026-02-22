/**
 * D-PlaneOS Password Strength Widget  v3.3.0
 *
 * API:
 *   PasswordStrength.validate(pw)          → { valid, missing[], message }
 *   PasswordStrength.enhance(inputEl)      → toggle + live requirement checklist
 *   PasswordStrength.toggle(inputEl)       → show/hide eye button only
 *   PasswordStrength.attach(inputEl)       → live requirement checklist only
 *   PasswordStrength.confirm(newEl, confEl)→ confirm-match indicator
 *
 * Rules mirror Go validatePasswordStrength exactly:
 *   ≥8 chars · uppercase · lowercase · digit · special char
 *
 * Behaviour change v3.3.0:
 *   Requirements are shown immediately on focus (not hidden until first keystroke).
 *   This ensures users know what is required BEFORE they start typing, eliminating
 *   the "guessing game" where a short password is submitted and silently rejected.
 */
(function (window) {
  'use strict';

  const RULES = [
    { key: 'length',  label: 'At least 8 characters',    test: pw => pw.length >= 8 },
    { key: 'upper',   label: 'Uppercase letter (A–Z)',    test: pw => /[A-Z]/.test(pw) },
    { key: 'lower',   label: 'Lowercase letter (a–z)',    test: pw => /[a-z]/.test(pw) },
    { key: 'digit',   label: 'Number (0–9)',               test: pw => /[0-9]/.test(pw) },
    { key: 'special', label: 'Special character (!@#…)',  test: pw => /[^A-Za-z0-9]/.test(pw) },
  ];

  function validate(pw) {
    pw = (pw || '').trim();
    const missing = RULES.filter(r => !r.test(pw)).map(r => r.label);
    return {
      valid: missing.length === 0,
      missing,
      message: missing.length ? 'Password must include: ' + missing.join(', ') : '',
    };
  }

  function toggle(inputEl) {
    if (!inputEl || inputEl.closest('.pw-wrap')) return;
    const wrap = document.createElement('div');
    wrap.className = 'pw-wrap';
    wrap.style.cssText = 'position:relative;display:flex;align-items:center;';
    inputEl.parentNode.insertBefore(wrap, inputEl);
    wrap.appendChild(inputEl);
    inputEl.style.paddingRight = '44px';
    inputEl.style.width = '100%';

    const btn = document.createElement('button');
    btn.type = 'button';
    btn.setAttribute('aria-label', 'Show password');
    btn.style.cssText = [
      'position:absolute;right:10px;top:50%;transform:translateY(-50%);',
      'background:none;border:none;padding:4px;cursor:pointer;',
      'color:rgba(255,255,255,0.4);line-height:1;',
      'font-family:"Material Symbols Rounded";font-size:20px;',
      'transition:color .15s cubic-bezier(0.4,0,0.2,1);',
      'display:flex;align-items:center;',
    ].join('');
    btn.textContent = 'visibility';
    btn.addEventListener('mouseenter', () => btn.style.color = 'rgba(255,255,255,0.85)');
    btn.addEventListener('mouseleave', () => {
      btn.style.color = inputEl.type === 'text' ? 'var(--md-sys-color-primary,#8a9cff)' : 'rgba(255,255,255,0.4)';
    });
    btn.addEventListener('click', () => {
      const show = inputEl.type === 'password';
      inputEl.type = show ? 'text' : 'password';
      btn.textContent = show ? 'visibility_off' : 'visibility';
      btn.setAttribute('aria-label', show ? 'Hide password' : 'Show password');
      btn.style.color = show ? 'var(--md-sys-color-primary,#8a9cff)' : 'rgba(255,255,255,0.4)';
    });
    wrap.appendChild(btn);
  }

  function attach(inputEl) {
    if (!inputEl) return;

    const widget = document.createElement('div');
    widget.className = 'pw-strength-checklist';
    widget.style.cssText = [
      'margin-top:8px;display:none;flex-direction:column;gap:5px;',
      'background:rgba(255,255,255,0.03);',
      'border:1px solid rgba(255,255,255,0.07);',
      'border-radius:var(--md-sys-shape-corner-small,8px);',
      'padding:10px 12px;',
    ].join('');

    const items = {};
    RULES.forEach(rule => {
      const row = document.createElement('div');
      row.style.cssText = 'display:flex;align-items:center;gap:8px;font-size:12px;color:rgba(255,255,255,0.4);transition:color .15s;';
      row.innerHTML = '<span class="pw-rule-icon" style="font-family:\'Material Symbols Rounded\';font-size:14px;line-height:1;">radio_button_unchecked</span><span>' + rule.label + '</span>';
      widget.appendChild(row);
      items[rule.key] = row;
    });

    const anchor = inputEl.closest('.pw-wrap') || inputEl;
    anchor.parentNode.insertBefore(widget, anchor.nextSibling);

    function update(forceShow) {
      const pw = inputEl.value;
      // Show on focus even if empty — requirements must be visible before typing
      if (!pw && !forceShow) {
        widget.style.display = 'none';
        return;
      }
      widget.style.display = 'flex';
      RULES.forEach(rule => {
        const pass = rule.test(pw);
        const row = items[rule.key];
        const icon = row.querySelector('.pw-rule-icon');
        if (pass) {
          row.style.color = '#10B981';
          icon.style.color = '#10B981';
          icon.textContent = 'check_circle';
        } else {
          row.style.color = 'rgba(255,255,255,0.4)';
          icon.style.color = '';
          icon.textContent = 'radio_button_unchecked';
        }
      });
    }

    // Show requirements immediately on focus — no more guessing
    inputEl.addEventListener('focus', () => update(true));
    inputEl.addEventListener('blur', () => { if (!inputEl.value) widget.style.display = 'none'; });
    inputEl.addEventListener('input', () => update(false));
    update(false);
  }

  function attachConfirm(newEl, confEl) {
    if (!newEl || !confEl) return;
    const feedback = document.createElement('div');
    feedback.style.cssText = 'font-size:12px;margin-top:5px;min-height:16px;transition:color .15s;';
    const anchor = confEl.closest('.pw-wrap') || confEl;
    anchor.parentNode.insertBefore(feedback, anchor.nextSibling);

    function check() {
      const a = newEl.value, b = confEl.value;
      if (!b) { feedback.textContent = ''; return; }
      if (a === b) {
        feedback.style.color = '#10B981';
        feedback.innerHTML = '<span style="font-family:\'Material Symbols Rounded\';font-size:13px;vertical-align:middle;line-height:1;">check_circle</span> Passwords match';
      } else {
        feedback.style.color = 'var(--error,#ff5252)';
        feedback.innerHTML = '<span style="font-family:\'Material Symbols Rounded\';font-size:13px;vertical-align:middle;line-height:1;">cancel</span> Passwords do not match';
      }
    }
    newEl.addEventListener('input', check);
    confEl.addEventListener('input', check);
    check();
  }

  function enhance(inputEl) { toggle(inputEl); attach(inputEl); }

  window.PasswordStrength = { validate, enhance, toggle, attach, confirm: attachConfirm };
})(window);
