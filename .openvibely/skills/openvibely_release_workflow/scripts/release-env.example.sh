#!/usr/bin/env bash
# Copy to .release-signing.env, fill in real values, then let release.sh load it.
# Do not commit secrets, certificates, passwords, or private keys.

OPENVIBELY_RELEASE_KEY_ID='openvibely-release-1'
OPENVIBELY_RELEASE_PUBLIC_KEY='<base64-ed25519-public-key>'

# macOS Developer ID signing + notarization.
OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Your Name or Company (TEAMID)'
OPENVIBELY_MACOS_NOTARY_PROFILE='openvibely-notary'

# Windows Authenticode signing from macOS via osslsigncode.
WINDOWS_CERT_P12="$HOME/secure/openvibely/windows-code-signing.pfx"
WINDOWS_CERT_PASSWORD='<certificate-password>'
OPENVIBELY_WINDOWS_SIGN_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/sign-windows.sh'
OPENVIBELY_WINDOWS_VERIFY_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/verify-windows.sh'

# Required if building the official release on macOS without a native Linux job.
OPENVIBELY_LINUX_DESKTOP_BINARY='<path-to-linux-amd64-openvibely-desktop>'
