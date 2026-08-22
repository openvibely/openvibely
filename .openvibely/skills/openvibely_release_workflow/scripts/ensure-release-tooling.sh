#!/usr/bin/env bash
set -euo pipefail

tool_runs() {
    local tool="$1"
    TOOL="$tool" bash -c '"$TOOL" --help >/dev/null 2>&1' 2>/dev/null
}

repo_root() {
    local root
    root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel 2>/dev/null | tail -n 1 || true)"
    if [[ -z "$root" ]]; then
        root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd -P)"
    fi
    printf '%s\n' "$root"
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

python_version_at_least() {
    local python="$1" min_major="$2" min_minor="$3"
    "$python" - "$min_major" "$min_minor" <<'PY'
import sys
major, minor = map(int, sys.argv[1:3])
raise SystemExit(0 if sys.version_info[:2] >= (major, minor) else 1)
PY
}

install_java_runtime() {
    local root tool_dir java_bin
    root="$(repo_root)"
    tool_dir="${root}/.tools/jre"
    java_bin="${tool_dir}/Contents/Home/bin/java"

    if command -v java >/dev/null 2>&1 && java -version >/dev/null 2>&1; then
        echo "ok tool: java ($(command -v java))"
        return 0
    fi

    if [[ -x "$java_bin" ]] && "$java_bin" -version >/dev/null 2>&1; then
        echo "ok tool: java ($java_bin)"
        return 0
    fi

    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "java runtime not found; install Java 17+ or set OPENVIBELY_JAVA_BIN" >&2
        return 1
    fi

    local arch adoptium_arch url tmp extracted jre_root
    arch="$(uname -m)"
    case "$arch" in
        arm64) adoptium_arch="aarch64" ;;
        x86_64) adoptium_arch="x64" ;;
        *)
            echo "unsupported macOS architecture for local Java runtime: $arch" >&2
            return 1
            ;;
    esac

    echo "java runtime not found; downloading local Temurin JRE..." >&2
    url="https://api.adoptium.net/v3/binary/latest/21/ga/mac/${adoptium_arch}/jre/hotspot/normal/eclipse"
    tmp="$(mktemp -d)"
    extracted="${tmp}/extract"
    mkdir -p "$extracted"
    curl -fL -o "${tmp}/jre.tar.gz" "$url"
    tar -xzf "${tmp}/jre.tar.gz" -C "$extracted"
    jre_root="$(find "$extracted" -type f -path '*/Contents/Home/bin/java' -print -quit | sed 's#/Contents/Home/bin/java$##')"
    if [[ -z "$jre_root" ]]; then
        echo "downloaded Java runtime did not contain Contents/Home/bin/java" >&2
        return 1
    fi
    rm -rf "$tool_dir"
    mkdir -p "$(dirname "$tool_dir")"
    mv "$jre_root" "$tool_dir"
    if [[ -x "$java_bin" ]] && "$java_bin" -version >/dev/null 2>&1; then
        echo "ok tool: java ($java_bin)"
        return 0
    fi
    echo "downloaded Java runtime is not runnable" >&2
    return 1
}

install_python_runtime() {
    local root tool_dir python_bin
    root="$(repo_root)"
    tool_dir="${root}/.tools/python"
    python_bin="${tool_dir}/bin/python3"

    if command -v python3 >/dev/null 2>&1 && python_version_at_least "$(command -v python3)" 3 13; then
        echo "ok tool: python3 ($(command -v python3))"
        return 0
    fi

    if [[ -x "$python_bin" ]] && python_version_at_least "$python_bin" 3 13; then
        echo "ok tool: python3 ($python_bin)"
        return 0
    fi

    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "python3.13 not found; install Python 3.13+ for local Azure CLI setup" >&2
        return 1
    fi
    if ! command -v python3 >/dev/null 2>&1; then
        echo "python3 is required to discover the local Python 3.13 release asset" >&2
        return 1
    fi

    local arch python_arch url tmp python_root
    arch="$(uname -m)"
    case "$arch" in
        arm64) python_arch="aarch64" ;;
        x86_64) python_arch="x86_64" ;;
        *)
            echo "unsupported macOS architecture for local Python runtime: $arch" >&2
            return 1
            ;;
    esac

    echo "python3.13 not found; downloading local Python 3.13 runtime..." >&2
    url="$(PYTHON_ARCH="$python_arch" python3 <<'PY'
import json
import os
import sys
import urllib.request

arch = os.environ["PYTHON_ARCH"]
api = "https://api.github.com/repos/astral-sh/python-build-standalone/releases/latest"
with urllib.request.urlopen(api, timeout=30) as response:
    release = json.load(response)

needle = f"{arch}-apple-darwin-install_only.tar.gz"
for asset in release.get("assets", []):
    name = asset.get("name", "")
    if name.startswith("cpython-3.13.") and needle in name:
        print(asset["browser_download_url"])
        break
else:
    sys.exit("no Python 3.13 macOS install_only asset found")
PY
)"
    if [[ -z "$url" ]]; then
        echo "could not discover Python 3.13 download URL" >&2
        return 1
    fi

    tmp="$(mktemp -d)"
    curl -fL -o "${tmp}/python.tar.gz" "$url"
    mkdir -p "${tmp}/extract"
    tar -xzf "${tmp}/python.tar.gz" -C "${tmp}/extract"
    if [[ -x "${tmp}/extract/python/bin/python3" || -x "${tmp}/extract/python/bin/python3.13" ]]; then
        python_root="${tmp}/extract/python"
    else
        python_root="$(find "${tmp}/extract" -type d -path '*/bin' -print -quit | sed 's#/bin$##')"
    fi
    if [[ -z "$python_root" ]]; then
        echo "downloaded Python runtime did not contain bin/python3" >&2
        return 1
    fi
    rm -rf "$tool_dir"
    mkdir -p "$(dirname "$tool_dir")"
    mv "$python_root" "$tool_dir"
    if [[ ! -e "${tool_dir}/bin/python3" && -x "${tool_dir}/bin/python3.13" ]]; then
        ln -s python3.13 "${tool_dir}/bin/python3"
    fi
    if [[ -x "$python_bin" ]] && python_version_at_least "$python_bin" 3 13; then
        echo "ok tool: python3 ($python_bin)"
        return 0
    fi
    echo "downloaded Python runtime is not runnable or is not Python 3.13+" >&2
    return 1
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
    repo_root="$(repo_root)"
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

install_jsign() {
    local repo_root
    repo_root="$(repo_root)"

    if command -v jsign >/dev/null 2>&1; then
        echo "ok tool: jsign ($(command -v jsign))"
        return 0
    fi

    local tool_dir="${repo_root}/.tools/jsign"
    local bin_dir="${tool_dir}/bin"
    local jar="${tool_dir}/jsign-7.5.jar"
    local wrapper="${bin_dir}/jsign"

    if [[ ! -f "$jar" ]]; then
        echo "jsign not found; downloading local jsign..." >&2
        local url="https://github.com/ebourg/jsign/releases/download/7.5/jsign-7.5.jar"
        local sha256="602a51c3545a6dc4fb99bd2ea7152b26d1345916d0c93ddfbd5936cb735af91c"
        local tmp actual
        tmp="$(mktemp -d)"
        curl -fsSL -o "${tmp}/jsign.jar" "$url"
        actual="$(shasum -a 256 "${tmp}/jsign.jar" | awk '{print $1}')"
        if [[ "$actual" != "$sha256" ]]; then
            echo "jsign download checksum mismatch" >&2
            echo "expected: $sha256" >&2
            echo "actual:   $actual" >&2
            return 1
        fi
        mkdir -p "$tool_dir"
        mv "${tmp}/jsign.jar" "$jar"
    fi

    mkdir -p "$bin_dir"
    cat > "$wrapper" <<EOF
#!/usr/bin/env bash
repo_root="$repo_root"
if [[ -n "\${OPENVIBELY_JAVA_BIN:-}" ]]; then
    exec "\$OPENVIBELY_JAVA_BIN" -jar "$jar" "\$@"
elif [[ -x "\${repo_root}/.tools/jre/Contents/Home/bin/java" ]]; then
    exec "\${repo_root}/.tools/jre/Contents/Home/bin/java" -jar "$jar" "\$@"
else
    exec java -jar "$jar" "\$@"
fi
EOF
    chmod +x "$wrapper"
    echo "ok tool: jsign ($wrapper)"
}

install_azure_cli() {
    if [[ -n "${AZURE_ACCESS_TOKEN:-}" ]]; then
        echo "ok env: AZURE_ACCESS_TOKEN (skipping az install)"
        return 0
    fi
    if command -v az >/dev/null 2>&1; then
        echo "ok tool: az ($(command -v az))"
        return 0
    fi

    local root tool_dir az_bin
    root="$(repo_root)"
    tool_dir="${root}/.tools/azure-cli"
    az_bin="${tool_dir}/bin/az"

    if [[ -x "$az_bin" ]]; then
        echo "ok tool: az ($az_bin)"
        return 0
    fi

    if [[ "$(uname -s)" != "Darwin" ]]; then
        echo "az not found; install Azure CLI or set AZURE_ACCESS_TOKEN" >&2
        return 1
    fi
    local python_bin
    python_bin="${root}/.tools/python/bin/python3"
    if [[ ! -x "$python_bin" ]]; then
        python_bin="$(command -v python3 2>/dev/null || true)"
    fi
    if [[ -z "$python_bin" ]] || ! python_version_at_least "$python_bin" 3 13; then
        echo "az not found; Azure CLI tarball install requires Python 3.13+ on macOS" >&2
        echo "run ensure-release-tooling.sh to install local Python 3.13, install Azure CLI manually, or set AZURE_ACCESS_TOKEN" >&2
        return 1
    fi

    local arch version url tmp
    arch="$(uname -m)"
    case "$arch" in
        arm64|x86_64) ;;
        *)
            echo "unsupported macOS architecture for Azure CLI tarball: $arch" >&2
            return 1
            ;;
    esac

    echo "az not found; downloading local Azure CLI tarball..." >&2
    version="$(curl -fsSL https://api.github.com/repos/Azure/azure-cli/releases/latest | sed -n 's/.*"tag_name":[[:space:]]*"azure-cli-\([^"]*\)".*/\1/p' | head -1)"
    if [[ -z "$version" ]]; then
        echo "could not determine latest Azure CLI version" >&2
        return 1
    fi
    url="https://github.com/Azure/azure-cli/releases/download/azure-cli-${version}/azure-cli-${version}-macos-${arch}.tar.gz"
    tmp="$(mktemp -d)"
    curl -fL -o "${tmp}/az.tar.gz" "$url"
    rm -rf "$tool_dir"
    mkdir -p "$tool_dir"
    tar -xzf "${tmp}/az.tar.gz" -C "$tool_dir"
    if [[ -f "${tool_dir}/az.completion.sh" ]]; then
        :
    fi
    if [[ -x "$az_bin" ]]; then
        echo "ok tool: az ($az_bin)"
        return 0
    fi
    echo "downloaded Azure CLI tarball did not contain bin/az" >&2
    return 1
}

install_osslsigncode
if [[ "${SKIP_AZURE_SIGNING_TOOLING:-0}" != "1" ]]; then
    install_java_runtime
    install_python_runtime
    install_jsign
    install_azure_cli
fi
