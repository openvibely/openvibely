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
  // A saved server's sign-in expired; send the user to sign in again.
  function reconnectTarget(value) {
    return typeof value === 'string' && value.startsWith('/servers?reconnect=') ? value : '';
  }
  window.openVibelyReconnect = {
    async fetch(response) {
      if (response.status !== 401) return false;
      let target = reconnectTarget(response.headers.get('HX-Redirect'));
      if (!target) {
        try { target = reconnectTarget((await response.clone().json()).reconnect); } catch (_) {}
      }
      if (!target) return false;
      location.assign(target);
      return true;
    },
    frame(data) {
      let target = '';
      try { target = reconnectTarget(JSON.parse(data).reconnect); } catch (_) {}
      if (!target) return false;
      location.assign(target);
      return true;
    }
  };
  window.openVibelyPermissionErrors = {
    async fetch(response) {
      if (response.status !== 403 || response.headers.get('X-OpenVibely-Permission-Denied') !== 'true') return false;
      const message = toastMessage(response.headers.get('HX-Trigger')) || bodyMessage(await response.clone().text()) || fallback;
      window.showToast?.(message, 'failed');
      return true;
    },
    htmx(xhr) {
      if (!xhr || xhr.status !== 403 || xhr.getResponseHeader('X-OpenVibely-Permission-Denied') !== 'true') return false;
      // HTMX processes HX-Trigger before firing responseError.
      if (!toastMessage(xhr.getResponseHeader('HX-Trigger'))) {
        window.showToast?.(bodyMessage(xhr.responseText) || fallback, 'failed');
      }
      return true;
    }
  };
})();
