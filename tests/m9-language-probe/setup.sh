#!/usr/bin/env bash
# Provision ONLY inside a root-owned job. This script does not submit jobs,
# build Huyang, start services, or run the probe. Invoke with: bash .../setup.sh PREFIX
set -euo pipefail
umask 077
bootstrap_python=$(command -v python3)
script_dir=$(dirname -- "$(realpath -e -- "${BASH_SOURCE[0]}")")
# No writes, including Python bytecode, before the source-owned HOME/namespace guard.
"$bootstrap_python" -I -B "$script_dir/safety.py" --fresh-prefix "${1:?usage: setup.sh JOB_LOCAL_PREFIX}"
bootstrap_python_version=$("$bootstrap_python" -I -B --version)
bootstrap_python_modules=$("$bootstrap_python" -I -B -c 'import venv,ensurepip; print("venv_stdlib="+venv.__file__); print("ensurepip_bundled_pip="+ensurepip.version())')
bootstrap_git=$(command -v git)
bootstrap_git_version=$("$bootstrap_git" --version)
prefix=$(realpath -m -- "${1:?usage: setup.sh JOB_LOCAL_PREFIX}")
[ "$prefix" != / ] || { echo "refusing root prefix" >&2; exit 1; }
case "$prefix/" in /tmp/*) echo "prefix must be an on-disk job-local directory" >&2; exit 1;; esac
[ "$(uname -m)" = x86_64 ] && [ "$(uname -s)" = Linux ] || { echo "Linux x86_64 required" >&2; exit 1; }
# HOME is never reassigned. Explicit config/cache paths isolate every writer.
mkdir -p "$prefix"/{bin,downloads,config,data,cache,state,tmp,npm,go}
export XDG_CONFIG_HOME=$prefix/config XDG_DATA_HOME=$prefix/data XDG_CACHE_HOME=$prefix/cache XDG_STATE_HOME=$prefix/state TMPDIR=$prefix/tmp
export UV_CACHE_DIR=$prefix/cache/uv UV_PYTHON_INSTALL_DIR=$prefix/python UV_TOOL_DIR=$prefix/uv-tools
export UV_PYTHON_BIN_DIR=$prefix/bin UV_PYTHON_INSTALL_BIN=0 UV_CREDENTIALS_DIR=$prefix/data/uv-credentials
export POETRY_CONFIG_DIR=$prefix/config/poetry POETRY_DATA_DIR=$prefix/data/poetry POETRY_CACHE_DIR=$prefix/cache/poetry
export POETRY_KEYRING_ENABLED=false NETRC=/dev/null
export PIP_CONFIG_FILE=/dev/null PIP_DISABLE_PIP_VERSION_CHECK=1
export NPM_CONFIG_USERCONFIG=/dev/null NPM_CONFIG_GLOBALCONFIG=/dev/null NPM_CONFIG_CACHE=$prefix/cache/npm
export GOPATH=$prefix/go GOMODCACHE=$prefix/go/pkg/mod GOCACHE=$prefix/cache/go HUYANG_FRICTION=0
export GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
unset VIRTUAL_ENV CONDA_PREFIX PYTHONPATH PYTHONHOME UV_PYTHON UV_PROJECT_ENVIRONMENT HUYANG_SOCKET HUYANG_STATE_DIR
unset UV_INDEX_URL UV_EXTRA_INDEX_URL UV_INDEX UV_DEFAULT_INDEX PIP_EXTRA_INDEX_URL
# Invoke under env -i as shown in the report; never inherit credential-bearing indexes.
cd "$prefix"
# Bootstrap prerequisites are recorded rather than silently installed on the
# host. Python3 needs stdlib venv+ensurepip; Go must satisfy this repository.
for command in curl tar xz awk chmod sha256sum python3 go strace git sh bash readlink uname dirname realpath; do
    command -v "$command" >/dev/null || { echo "missing job prerequisite $command" >&2; exit 1; }
done
nvim_version=0.12.5
nvim_sha=bce0f56eda1f1b1db6eee8f4133d7a38813ea07933837dd1777411ca384c6875
lsp_commit=19576de1c1b6d1b5e231ea6ef9cba9a811c322bb
lsp_sha=e18b21ee7bb000817b1337e9455d6fd719049e2c7b47e550949804d5fceda8fb
node_version=22.14.0
uv_version=0.9.5
poetry_version=2.2.1
python_version=3.12.8
gopls_version=v0.23.0
pyright_version=1.1.413
resolution_cutoff=2026-10-16T00:00:00Z
fetch() {
    local file=$1 sha=$2 url=$3
    curl -q --fail --silent --show-error --location --connect-timeout 30 --max-time 300 "$url" -o "$prefix/downloads/$file"
    printf '%s  %s\n' "$sha" "$prefix/downloads/$file" | sha256sum --check --status
}
fetch nvim.tar.gz "$nvim_sha" "https://github.com/neovim/neovim/releases/download/v$nvim_version/nvim-linux-x86_64.tar.gz"
mkdir -p "$prefix/nvim"
tar --no-same-owner -xzf downloads/nvim.tar.gz -C "$prefix/nvim" --strip-components=1
ln -sfn "$prefix/nvim/bin/nvim" "$prefix/bin/nvim"
fetch lspconfig.tar.gz "$lsp_sha" "https://github.com/neovim/nvim-lspconfig/archive/$lsp_commit.tar.gz"
mkdir -p "$prefix/nvim-lspconfig"
tar --no-same-owner -xzf downloads/lspconfig.tar.gz -C "$prefix/nvim-lspconfig" --strip-components=1
# Exact Node version, checked against its versioned vendor SHA256 manifest.
node_file=node-v$node_version-linux-x64.tar.xz
curl -q --fail --silent --show-error --location --connect-timeout 30 --max-time 300 "https://nodejs.org/dist/v$node_version/SHASUMS256.txt" -o downloads/node-SHASUMS256.txt
node_sha=$(awk -v f="$node_file" '$2 == f {print $1}' downloads/node-SHASUMS256.txt)
[ "${#node_sha}" = 64 ] || { echo "Node checksum missing" >&2; exit 1; }
fetch "$node_file" "$node_sha" "https://nodejs.org/dist/v$node_version/$node_file"
mkdir -p "$prefix/node"
tar --no-same-owner -xJf "downloads/$node_file" -C "$prefix/node" --strip-components=1
ln -sfn "$prefix/node/bin/node" "$prefix/bin/node"
export PATH=$prefix/bin:$PATH
printf '{"name":"m9-probe-tools","private":true}\n' > npm/package.json
"$prefix/node/bin/npm" install --prefix "$prefix/npm" --save-exact --ignore-scripts --no-audit --no-fund \
    --before="$resolution_cutoff" "pyright@$pyright_version"
ln -sfn "$prefix/npm/node_modules/.bin/pyright-langserver" "$prefix/bin/pyright-langserver"
# Bootstrap uv is installed into an isolated venv; never into the host Python.
"$bootstrap_python" -I -B -m venv "$prefix/bootstrap"
bootstrap_pip_version=$("$prefix/bootstrap/bin/python" -I -B -m pip --version)
"$prefix/bootstrap/bin/python" -I -m pip --isolated install --index-url https://pypi.org/simple --cache-dir "$prefix/cache/pip" "uv==$uv_version"
ln -sfn "$prefix/bootstrap/bin/uv" "$prefix/bin/uv"
"$prefix/bin/uv" --no-config python install --no-bin "$python_version"
base=$("$prefix/bin/uv" --no-config python find --managed-python "$python_version")
ln -sfn "$base" "$prefix/bin/python"
ln -sfn "$base" "$prefix/bin/python3"
"$prefix/bin/uv" --no-config venv --python "$base" "$prefix/poetry-tool"
"$prefix/bin/uv" --no-config pip install --python "$prefix/poetry-tool/bin/python" --index-url https://pypi.org/simple \
    --exclude-newer "$resolution_cutoff" "poetry==$poetry_version"
ln -sfn "$prefix/poetry-tool/bin/poetry" "$prefix/bin/poetry"
GOBIN=$prefix/bin go install "golang.org/x/tools/gopls@$gopls_version"
for command in strace git sh bash readlink uname dirname realpath; do
    ln -sfn "$(realpath -e "$(command -v "$command")")" "$prefix/bin/$command"
done
{
    printf 'nvim=%s\nlspconfig=%s\nnode=%s\nuv=%s\npoetry=%s\npython=%s\ngopls=%s\npyright=%s\nresolution_cutoff=%s\n' \
        "$nvim_version" "$lsp_commit" "$node_version" "$uv_version" "$poetry_version" "$python_version" "$gopls_version" "$pyright_version" "$resolution_cutoff"
    printf 'bootstrap_python_path=%s\nbootstrap_python=%s\n%s\nbootstrap_pip=%s\nbootstrap_git_path=%s\nbootstrap_git=%s\n' \
        "$bootstrap_python" "$bootstrap_python_version" "$bootstrap_python_modules" "$bootstrap_pip_version" "$bootstrap_git" "$bootstrap_git_version"
    "$prefix/bin/python" --version
    go version
    "$prefix/bin/strace" --version
    "$prefix/poetry-tool/bin/python" -I -m pip freeze 2>/dev/null || \
        "$prefix/bin/uv" --no-config pip freeze --python "$prefix/poetry-tool/bin/python"
} > "$prefix/manifest.txt"
find "$prefix/downloads" -maxdepth 1 -type f -exec sha256sum {} + > "$prefix/download-sha256.txt"
# Shared managed Python is read-only during acceptance; the probe also enforces
# full before/after tool hashes, so chmod alone is not the immutability proof.
chmod -R a-w "$prefix/python"
echo "Provisioned $prefix; this did not build or run Huyang."
