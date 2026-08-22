#!/usr/bin/env bash
# Copy to .release-signing.env, fill in real values, then let release.sh load it.
# Do not commit secrets, certificates, passwords, or private keys.

OPENVIBELY_RELEASE_KEY_ID='openvibely-release-1'
OPENVIBELY_RELEASE_PUBLIC_KEY='<base64-ed25519-public-key>'

# macOS Developer ID signing + notarization.
OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Your Name or Company (TEAMID)'
OPENVIBELY_MACOS_NOTARY_PROFILE='openvibely-notary'

# Windows Authenticode signing from macOS via Azure Artifact Signing + jsign.
OPENVIBELY_AZURE_SIGNING_ENDPOINT='<azure-region>.codesigning.azure.net'
OPENVIBELY_AZURE_SIGNING_ACCOUNT='<artifact-signing-account-name>'
OPENVIBELY_AZURE_SIGNING_PROFILE='<certificate-profile-name>'
OPENVIBELY_AZURE_SUBSCRIPTION_ID='<optional-azure-subscription-id>'
OPENVIBELY_WINDOWS_SIGN_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/sign-windows.sh'
OPENVIBELY_WINDOWS_VERIFY_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/verify-windows.sh'

# Required if building the official release on macOS without a native Linux job.
OPENVIBELY_LINUX_DESKTOP_BINARY='<path-to-linux-amd64-openvibely-desktop>'
OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY='<path-to-linux-arm64-openvibely-desktop>'
