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

You need a Windows code-signing certificate as `.p12` or `.pfx`.

1. Put the certificate outside the repo, for example:

```bash
mkdir -p ~/secure/openvibely
```

2. Put these in `.release-signing.env`:

```bash
WINDOWS_CERT_P12="$HOME/secure/openvibely/windows-code-signing.pfx"
WINDOWS_CERT_PASSWORD='<certificate-password>'
OPENVIBELY_WINDOWS_SIGN_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/sign-windows.sh'
OPENVIBELY_WINDOWS_VERIFY_COMMAND='/absolute/path/to/openvibely/.openvibely/skills/openvibely_release_workflow/scripts/verify-windows.sh'
```

## Check Readiness

```bash
.openvibely/skills/openvibely_release_workflow/scripts/check-release-signing.sh
```

On macOS, the scripts download the official native `osslsigncode` release into `.tools/osslsigncode` when it is not already installed. No Homebrew, Docker, or Java is required. The check must pass before publishing official macOS or Windows auto-update artifacts.

The normal release command runs this setup/check automatically:

```bash
.openvibely/skills/openvibely_release_workflow/scripts/release.sh <version>
```
