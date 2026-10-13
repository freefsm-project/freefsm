(() => {
  'use strict';
  const clear = form => form.querySelectorAll('[data-clock-coordinate]').forEach(input => input.remove());
  document.addEventListener('submit', event => {
    if (event.submitter?.hasAttribute('data-clock-free')) clear(event.target);
  }, true);
  document.addEventListener('click', event => {
    const button = event.target.closest('[data-clock-gps]');
    if (!button || button.disabled) return;
    const form = button.form;
    const status = form.querySelector('[data-clock-location-status]');
    clear(form);
    const unavailable = () => {
      button.disabled = false;
      status.textContent = 'Location unavailable. You can explicitly clock without location.';
    };
    if (!navigator.geolocation) { unavailable(); return; }
    button.disabled = true;
    status.textContent = 'Requesting one location capture…';
    navigator.geolocation.getCurrentPosition(position => {
      const {latitude, longitude, accuracy} = position.coords;
      if (!Number.isFinite(latitude) || latitude < -90 || latitude > 90 ||
          !Number.isFinite(longitude) || longitude < -180 || longitude > 180 ||
          !Number.isFinite(accuracy) || accuracy < 0 ||
          !Number.isFinite(position.timestamp) || position.timestamp > Date.now()) {
        button.disabled = false;
        status.textContent = 'Invalid location. You can explicitly clock without location.';
        return;
      }
      for (const [name,value] of Object.entries({latitude,longitude,accuracy,captured_at:new Date(position.timestamp).toISOString()})) {
        const input = document.createElement('input');
        input.type = 'hidden'; input.name = name; input.value = String(value);
        input.setAttribute('data-clock-coordinate', ''); form.append(input);
      }
      button.disabled = false;
      form.requestSubmit();
    }, unavailable, {enableHighAccuracy:true, maximumAge:0, timeout:10000});
  });
})();
