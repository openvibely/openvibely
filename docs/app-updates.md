# App updates

Open **Settings → App updates** to enable or disable automatic updates. Automatic updates are enabled by default, including existing installations without a saved preference. The setting applies to the entire installation, persists across restarts, and can be changed by an administrator.

On supported binary, desktop, and Docker-agent installations, the background updater checks for releases using the existing daily check interval and retry policy. It verifies and stages eligible releases, stops admitting new work, waits for running tasks, chats, and automation activities to finish, then installs and restarts using the existing recovery and rollback flow.

Disabling automatic updates prevents new automatic installations. An update already accepted continues to completion. Manual update acceptance remains available through the existing update controls. Update notification visibility is controlled separately by `DISABLE_UPDATE_NOTIFICATIONS`; hiding notifications does not disable automatic installation.

A version automatically accepted once is not automatically accepted again after failure, cancellation, or rollback, including after restarting. Manual retry remains available. A newer release can be installed automatically.

Source builds and manually deployed Docker installations still require manual deployment. Hosted installations remain controlled by the hosting administrator. The settings page indicates when automatic installation is unavailable.
