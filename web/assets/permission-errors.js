// Permission failures are notifications, not replacements for the current UI.
(function () {
  function toastMessage(header) {
    try {
      const toast = JSON.parse(header || '{}').openvibelyToast;
      return typeof toast?.message === 'string' ? toast.message : '';
    } catch (_) { return ''; }
  }
  function bodyMessage(body) {
    try {
      const parsed = JSON.parse(body);
      return typeof parsed.message === 'string' ? parsed.message : '';
    } catch (_) { return ''; }
  }
  const fallback = 'You do not have permission to perform this action. Ask an organization admin for access.';
  window.openVibelyPermissionErrors = {
    async fetch(response) {
      if (response.status !== 403) return false;
      const message = toastMessage(response.headers.get('HX-Trigger')) || bodyMessage(await response.clone().text()) || fallback;
      window.showToast?.(message, 'failed');
      return true;
    },
    htmx(xhr) {
      if (!xhr || xhr.status !== 403) return false;
      // HTMX processes HX-Trigger before firing responseError.
      if (!toastMessage(xhr.getResponseHeader('HX-Trigger'))) {
        window.showToast?.(bodyMessage(xhr.responseText) || fallback, 'failed');
      }
      return true;
    }
  };
})();
