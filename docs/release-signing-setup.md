# Release Signing Setup

OpenVibely public desktop releases require OS signing before auto-update is enabled.

## macOS

You need an Apple Developer Program account.

1. Create a Developer ID Application certificate.
2. Create a notarytool profile:

```bash
.openvibely/skills/openvibely_release_workflow/scripts/check-release-signing.sh --setup
```

This creates `.release-signing.env` if it does not exist. Fill in the placeholders once; future checks load it automatically.

3. Put these in `.release-signing.env`:

```bash
OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Your Name or Company (TEAMID)'
OPENVIBELY_MACOS_NOTARY_PROFILE='openvibely-notary'
```

## Windows From macOS

You need an Azure Artifact Signing account with completed identity validation
and an active Public Trust certificate profile.

Put these in `.release-signing.env`:

```bash
OPENVIBELY_AZURE_SIGNING_ENDPOINT='<azure-region>.codesigning.azure.net'
OPENVIBELY_AZURE_SIGNING_ACCOUNT='<artifact-signing-account-name>'
OPENVIBELY_AZURE_SIGNING_PROFILE='<certificate-profile-name>'
OPENVIBELY_AZURE_SUBSCRIPTION_ID='<optional-azure-subscription-id>'
OPENVIBELY_WINDOWS_SIGN_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/sign-windows.sh'
OPENVIBELY_WINDOWS_VERIFY_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/verify-windows.sh'
```

The signer uses `jsign` locally on macOS and obtains a short-lived Azure access
token with `az account get-access-token`. If `AZURE_ACCESS_TOKEN` is already set,
the script uses that token and does not call `az`.

## Check Readiness

```bash
.openvibely/skills/openvibely_release_workflow/scripts/check-release-signing.sh
```

On macOS, the scripts install local release tooling under `.tools/` when needed:

- `.tools/osslsigncode` for Windows signature verification.
- `.tools/jsign` for Azure Artifact Signing.
- `.tools/jre` for the Java runtime used by `jsign` when no runnable system Java
  is present.
- `.tools/azure-cli` for `az` when the Azure CLI is absent and Python 3.13+ is
  available.

If Python 3.13+ is not available, install Azure CLI manually or provide
`AZURE_ACCESS_TOKEN` before signing. No Homebrew or Docker is required by the
release scripts. The check must pass before publishing official macOS or Windows
auto-update artifacts.

The normal release command runs this setup/check automatically:

```bash
.openvibely/skills/openvibely_release_workflow/scripts/release.sh <version>
```
