#!/usr/bin/env bash
set -euo pipefail

tool_runs() {
    local tool="$1"
    TOOL="$tool" bash -c '"$TOOL" --help >/dev/null 2>&1' 2>/dev/null
}

download_ghcr_blob() {
    local scope="$1" url="$2" output="$3"
    local token
    token="$(curl -fsSL "https://ghcr.io/token?scope=${scope}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
    if [[ -z "$token" ]]; then
        echo "could not obtain GHCR token for ${scope}" >&2
        return 1
    fi
    curl -fsSL -H "Authorization: Bearer ${token}" -o "$output" "$url"
}

install_macos_openssl_runtime() {
    local tool_dir="$1"
    local lib_dir="${tool_dir}/lib"
    local modules_dir="${lib_dir}/ossl-modules"
    local arch os_major bottle_url bottle_sha

    arch="$(uname -m)"
    os_major="$(sw_vers -productVersion | awk -F. '{print $1}')"

    case "${arch}:${os_major}" in
        arm64:26)
            bottle_url="https://ghcr.io/v2/homebrew/core/openssl/3/blobs/sha256:2d995a1bbbd8e6ee6a9042990dde87e7321d1ddd5716ffee53b140d23cb9f92f"
            bottle_sha="2d995a1bbbd8e6ee6a9042990dde87e7321d1ddd5716ffee53b140d23cb9f92f"
            ;;
        arm64:15)
            bottle_url="https://ghcr.io/v2/homebrew/core/openssl/3/blobs/sha256:f8b0b5b2eda9321265b7483ce7ea167012c32afed73a127e8e18ebe9f1d9dffe"
            bottle_sha="f8b0b5b2eda9321265b7483ce7ea167012c32afed73a127e8e18ebe9f1d9dffe"
            ;;
        arm64:14)
            bottle_url="https://ghcr.io/v2/homebrew/core/openssl/3/blobs/sha256:79774ba3c854f0a9f94d939c628414c9b3dd2ff5eeb1dc61743199c979dd3490"
            bottle_sha="79774ba3c854f0a9f94d939c628414c9b3dd2ff5eeb1dc61743199c979dd3490"
            ;;
        x86_64:*)
            echo "automatic macOS osslsigncode setup currently supports Apple Silicon only; install a runnable x86_64 osslsigncode manually or run release signing on arm64 macOS" >&2
            return 1
            ;;
        *)
            echo "unsupported macOS runtime for local OpenSSL bottle: arch=${arch} macOS=${os_major}" >&2
            return 1
            ;;
    esac

    local tmp actual extracted
    tmp="$(mktemp -d)"
    extracted="${tmp}/extract"
    download_ghcr_blob "repository:homebrew/core/openssl/3:pull" "$bottle_url" "${tmp}/openssl.tar.gz"
    actual="$(shasum -a 256 "${tmp}/openssl.tar.gz" | awk '{print $1}')"
    if [[ "$actual" != "$bottle_sha" ]]; then
        echo "OpenSSL bottle checksum mismatch" >&2
        echo "expected: $bottle_sha" >&2
        echo "actual:   $actual" >&2
        return 1
    fi

    mkdir -p "$extracted" "$lib_dir" "$modules_dir"
    chmod -R u+w "$lib_dir" 2>/dev/null || true
    tar -xzf "${tmp}/openssl.tar.gz" -C "$extracted" \
        openssl@3/3.6.3/lib/libssl.3.dylib \
        openssl@3/3.6.3/lib/libcrypto.3.dylib \
        openssl@3/3.6.3/lib/ossl-modules/legacy.dylib
    cp "${extracted}/openssl@3/3.6.3/lib/libssl.3.dylib" "$lib_dir/"
    cp "${extracted}/openssl@3/3.6.3/lib/libcrypto.3.dylib" "$lib_dir/"
    cp "${extracted}/openssl@3/3.6.3/lib/ossl-modules/legacy.dylib" "$modules_dir/"

    install_name_tool -id @loader_path/libcrypto.3.dylib "${lib_dir}/libcrypto.3.dylib"
    install_name_tool -id @loader_path/libssl.3.dylib "${lib_dir}/libssl.3.dylib"
    install_name_tool -change '@@HOMEBREW_CELLAR@@/openssl@3/3.6.3/lib/libcrypto.3.dylib' @loader_path/libcrypto.3.dylib "${lib_dir}/libssl.3.dylib"
    install_name_tool -change '@@HOMEBREW_PREFIX@@/opt/openssl@3/lib/libssl.3.dylib' @loader_path/libssl.3.dylib "${lib_dir}/libssl.3.dylib"
    install_name_tool -change '@@HOMEBREW_CELLAR@@/openssl@3/3.6.3/lib/libcrypto.3.dylib' @loader_path/../libcrypto.3.dylib "${modules_dir}/legacy.dylib"
    install_name_tool -change '@@HOMEBREW_PREFIX@@/opt/openssl@3/lib/libcrypto.3.dylib' @loader_path/../libcrypto.3.dylib "${modules_dir}/legacy.dylib"

    codesign --force --sign - "${lib_dir}/libssl.3.dylib" "${lib_dir}/libcrypto.3.dylib" "${modules_dir}/legacy.dylib" >/dev/null
}

macos_runtime_needs_patch() {
    local tool_dir="$1"
    local legacy="${tool_dir}/lib/ossl-modules/legacy.dylib"
    if [[ ! -f "$legacy" ]]; then
        return 0
    fi
    otool -L "$legacy" 2>/dev/null | grep -q '@@HOMEBREW'
}

patch_macos_osslsigncode_runtime() {
    local tool_dir="$1" tool_bin="$2"
    install_macos_openssl_runtime "$tool_dir"
    install_name_tool -change /opt/homebrew/opt/openssl@3/lib/libssl.3.dylib @loader_path/../lib/libssl.3.dylib "$tool_bin"
    install_name_tool -change /opt/homebrew/opt/openssl@3/lib/libcrypto.3.dylib @loader_path/../lib/libcrypto.3.dylib "$tool_bin"
    codesign --force --sign - "$tool_bin" >/dev/null
}

install_osslsigncode() {
    local repo_root
    repo_root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel 2>/dev/null | tail -n 1 || true)"
    if [[ -z "$repo_root" ]]; then
        repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd -P)"
    fi
    local tool_dir="${repo_root}/.tools/osslsigncode"
    local tool_bin="${tool_dir}/bin/osslsigncode"

    if [[ "$(uname -s)" != "Darwin" ]] && command -v osslsigncode >/dev/null 2>&1; then
        if tool_runs "$(command -v osslsigncode)"; then
            echo "ok tool: osslsigncode ($(command -v osslsigncode))"
            return 0
        fi
        echo "found osslsigncode but it is not runnable; continuing with local tool setup..." >&2
    fi

    if [[ -x "$tool_bin" ]]; then
        if tool_runs "$tool_bin"; then
            if [[ "$(uname -s)" == "Darwin" ]] && macos_runtime_needs_patch "$tool_dir"; then
                echo "local osslsigncode needs OpenSSL runtime repair; installing local runtime..." >&2
                patch_macos_osslsigncode_runtime "$tool_dir" "$tool_bin"
            fi
            echo "ok tool: osslsigncode ($tool_bin)"
            return 0
        fi
        echo "found local osslsigncode but it is not runnable; reinstalling..." >&2
    fi

    echo "osslsigncode not found; attempting local install..." >&2

    case "$(uname -s)" in
        Darwin)
            if [[ "$(uname -m)" != "arm64" ]]; then
                echo "automatic macOS osslsigncode setup currently supports Apple Silicon only; install a runnable x86_64 osslsigncode manually or run release signing on arm64 macOS" >&2
                return 1
            fi
            local url="https://github.com/mtrojnar/osslsigncode/releases/download/2.14/osslsigncode-2.14-macOS.zip"
            local sha256="555326a4d01c60c63bb0fd70d4c0eea66fd69036dd75a4f909f2ddf264f44a9c"
            local tmp actual
            tmp="$(mktemp -d)"
            curl -fsSL -o "${tmp}/osslsigncode.zip" "$url"
            actual="$(shasum -a 256 "${tmp}/osslsigncode.zip" | awk '{print $1}')"
            if [[ "$actual" != "$sha256" ]]; then
                echo "osslsigncode download checksum mismatch" >&2
                echo "expected: $sha256" >&2
                echo "actual:   $actual" >&2
                return 1
            fi
            mkdir -p "$tool_dir"
            unzip -q -o "${tmp}/osslsigncode.zip" -d "$tool_dir"
            chmod +x "$tool_bin"
            patch_macos_osslsigncode_runtime "$tool_dir" "$tool_bin"
            if tool_runs "$tool_bin"; then
                echo "ok tool: osslsigncode ($tool_bin)"
                return 0
            fi
            echo "downloaded osslsigncode is not runnable on this machine." >&2
            return 1
            ;;
        Linux)
            if command -v apt-get >/dev/null 2>&1; then
                sudo apt-get update
                sudo apt-get install -y osslsigncode
                return 0
            fi
            if command -v dnf >/dev/null 2>&1; then
                sudo dnf install -y osslsigncode
                return 0
            fi
            if command -v yum >/dev/null 2>&1; then
                sudo yum install -y osslsigncode
                return 0
            fi
            if command -v pacman >/dev/null 2>&1; then
                sudo pacman -Sy --noconfirm osslsigncode
                return 0
            fi
            if command -v zypper >/dev/null 2>&1; then
                sudo zypper --non-interactive install osslsigncode
                return 0
            fi
            ;;
    esac

    echo "Could not provide osslsigncode." >&2
    return 127
}

install_osslsigncode
