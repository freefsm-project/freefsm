(function () {
  if (window.__freefsmPasswordFieldsInstalled) return;
  window.__freefsmPasswordFieldsInstalled = true;

  document.addEventListener('click', function (event) {
    var button = event.target.closest('[data-password-toggle]');
    if (!button) return;

    var input = document.getElementById(button.getAttribute('aria-controls'));
    if (!input || input.tagName !== 'INPUT' || (input.type !== 'password' && input.type !== 'text')) return;

    var revealing = input.type === 'password';
    input.type = revealing ? 'text' : 'password';
    button.setAttribute('aria-pressed', revealing ? 'true' : 'false');
    button.setAttribute('aria-label', revealing ? 'Hide password' : 'Show password');

    var label = button.querySelector('[data-password-toggle-label]');
    if (label) label.textContent = revealing ? 'Hide' : 'Show';

    try {
      input.focus({ preventScroll: true });
    } catch (_) {
      input.focus();
    }
  });
})();
