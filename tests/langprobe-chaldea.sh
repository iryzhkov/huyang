#!/usr/bin/env bash
# Job-local prerequisites and runner for the nine-language probe
# (TestLangprobeABCD in cmd/huyang/langprobe_test.go).
#
# Everything is downloaded into a prefix inside the repository checkout
# (tests/.deps/langprobe by default, which .gitignore already excludes), so a
# Chaldea job provisions itself and nothing is installed on the host. The
# installed Huyang service is never contacted: the probe test starts its own
# private service, socket and state directory.
#
# Modes:
#   setup   download, verify and unpack the pinned toolchain into the prefix
#   check   setup, then verify every executable and run a headless Neovim
#           smoke that each of the nine language servers attaches and
#           publishes the expected diagnostic (no Huyang code involved)
#   probe   check, then run TestLangprobeABCD with HUYANG_LANGPROBE_STRICT=1
#           and fail unless all nine languages pass; a skip is a failure
#   suite [GO TEST ARGS]
#           setup, then go test -count=1 (default ./...) with Neovim and
#           every language server available
#   race [GO TEST ARGS]
#           as suite, with -race
#   run CMD...
#           setup, then run CMD from the repository root in the same
#           environment (for example: run go vet ./...)
#   env     print the environment exports for an interactive shell
#   selftest
#           check the /tmp and inherited-override guards locally; no
#           downloads and no jobs
#   submit [MODE [ARGS]]
#           run MODE (default probe) once on a job host through chaldea
#           exec and return the report; never retries, and passes the exit
#           status through (a busy refusal is reported for the caller to
#           schedule a Chaldea wait)
#
# Every mode writes tests/.deps/langprobe/out/summary.txt and
# tests/.deps/langprobe/out/langprobe-report.tar.gz, which submit requests
# as Chaldea artifacts.
#
# Optional environment:
#   LANGPROBE_PREFIX     tool prefix (default tests/.deps/langprobe)
#   LANGPROBE_LABEL      free text recorded in the summary, for example
#                        "baseline 6188357, not M1 acceptance"
#   LANGPROBE_LANGUAGES  comma-separated subset, for diagnosis only: the probe
#                        then runs without HUYANG_LANGPROBE_STRICT and the
#                        summary marks it partial, never acceptance
#   LANGPROBE_TIMEOUT    go test timeout (default 45m)
#   LANGPROBE_HOST       submit host (default agent-a)
#   LANGPROBE_SKIP_SMOKE set to 1 to skip the Neovim smoke in probe mode
#   LANGPROBE_TMPDIR     short canonical TMPDIR (default ~/.cache/hf, else
#                        the prefix's tmp when HOME is not writable); never
#                        /tmp or below, and the inherited TMPDIR is ignored
#
# The submit log goes to ~/.cache/huyang-langprobe. No path the script
# writes may resolve to /tmp or below.
set -euo pipefail

# Tests must never write the real friction spool.
export HUYANG_FRICTION=0
# Huyang refuses group- or world-writable runtime and state directories.
umask 022

# scratch_dir PATH creates PATH and prints its physical path, refusing /tmp
# and anything below it: host /tmp is a shared RAM-backed tmpfs. The
# destination is resolved before anything is created (realpath -m follows
# symbolic links in the existing ancestors and accepts missing components),
# so a link to /tmp higher up cannot make mkdir create a directory there.
# Only the validated canonical path is created, and it is checked again after.
scratch_dir() {
	local given=$1 dir tmp_root
	command -v realpath >/dev/null || {
		printf 'langprobe-chaldea: realpath is needed to validate %s\n' "$given" >&2
		return 1
	}
	tmp_root=$(realpath -m /tmp)
	dir=$(realpath -m -- "$given") || return 1
	case $given/ in /tmp/* | "$tmp_root"/*)
		printf 'langprobe-chaldea: refusing %s: under /tmp\n' "$given" >&2
		return 1
		;;
	esac
	case $dir/ in /tmp/* | "$tmp_root"/*)
		printf 'langprobe-chaldea: refusing %s: resolves to %s, under /tmp\n' "$given" "$dir" >&2
		return 1
		;;
	esac
	mkdir -p -- "$dir" 2>/dev/null || return 1
	dir=$(cd -- "$dir" && pwd -P) || return 1
	case $dir/ in /tmp/* | "$tmp_root"/*)
		printf 'langprobe-chaldea: refusing %s: resolves under /tmp\n' "$dir" >&2
		return 1
		;;
	esac
	printf '%s\n' "$dir"
}

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$script_dir/.." && pwd)
prefix=$(scratch_dir "${LANGPROBE_PREFIX:-$repo/tests/.deps/langprobe}") || {
	printf 'langprobe-chaldea: FAILED: unusable prefix %s\n' "${LANGPROBE_PREFIX:-$repo/tests/.deps/langprobe}" >&2
	exit 1
}
downloads=$prefix/downloads
out=$prefix/out
languages_all=go,rust,python,typescript,javascript,java,cpp,c,bash

# Pins. The npm, gopls, rust-analyzer and jdtls versions match the Mason
# packages behind the 2026-10-16 probe on gaming-pc; Neovim matches its
# 0.12.5; clangd is the upstream 22.x build closest to its system 22.1.8.
nvim_version=0.12.5
nvim_sha256=bce0f56eda1f1b1db6eee8f4133d7a38813ea07933837dd1777411ca384c6875
lspconfig_commit=19576de1c1b6d1b5e231ea6ef9cba9a811c322bb
lspconfig_sha256=e18b21ee7bb000817b1337e9455d6fd719049e2c7b47e550949804d5fceda8fb
gopls_version=v0.23.0
rust_analyzer_version=2026-09-07
rust_analyzer_sha256=a3500183aa08bf740c0da6e030ad262d4cfa1c19e7ce195ab5f772bdf9ddfb12
rust_src_version=1.99.0
rust_src_sha256=3f1f9b7ed48f4596fc87889b7b3c61747336a55c9c22db1ab0c697e0aadb77aa
clangd_version=22.1.6
clangd_sha256=a9c77443af2e447ed467e84771848d3a6ac1c56f84bcfcde717e66318de77cfa
shellcheck_version=v0.11.0
shellcheck_sha256=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198
jdk_release=jdk-21.0.12.1+1
jdk_file=OpenJDK21U-jdk_x64_linux_hotspot_21.0.12.1_1.tar.gz
jdk_sha256=ce79869e1307ed8ee1e2baa86a412b1eb5b75d10a01006d788a6f968bcfaee94
jdtls_file=jdt-language-server-1.60.0-202606262232.tar.gz
jdtls_sha256=e94c303d8198f977930803582738771fd18c52c5492878410bf222b1aa81ef1d
npm_before=2026-10-16T00:00:00Z
npm_packages=(pyright@1.1.413 typescript-language-server@6.0.0 typescript@6.0.3 bash-language-server@5.6.0)

log() { printf 'langprobe-chaldea: %s\n' "$*" >&2; }
die() {
	log "FAILED: $*"
	{ printf 'FAILED: %s\n' "$*" >>"$out/failures.txt"; } 2>/dev/null || true
	exit 1
}

export_env() {
	# go test puts Unix sockets under t.TempDir(), and a socket path must fit
	# in 108 bytes, so tests get a short TMPDIR. It is a real directory, never
	# a symbolic link, because Huyang compares canonical paths.
	# The inherited TMPDIR is never used: it may be host /tmp.
	local tmp
	if [ -n "${LANGPROBE_TMPDIR:-}" ]; then
		tmp=$(scratch_dir "$LANGPROBE_TMPDIR") || die "LANGPROBE_TMPDIR $LANGPROBE_TMPDIR is not usable"
	else
		tmp=$(scratch_dir "$HOME/.cache/hf") || tmp=$(scratch_dir "$prefix/tmp") ||
			die "neither $HOME/.cache/hf nor $prefix/tmp is usable as TMPDIR"
	fi
	[ "${#tmp}" -le 48 ] || die "TMPDIR $tmp is ${#tmp} bytes; go test socket paths need at most 48 (set LANGPROBE_TMPDIR)"
	export TMPDIR=$tmp
	# HOME stays the invoking user's. Every tool that would write under it
	# is pointed at the prefix instead: Neovim, gopls, clangd and tsserver
	# through the XDG directories, Maven and Java preferences through jdtls
	# JVM arguments (nvim-lspconfig reads JDTLS_JVM_ARGS).
	export XDG_CONFIG_HOME=$prefix/xdg/config XDG_DATA_HOME=$prefix/xdg/data
	export XDG_CACHE_HOME=$prefix/xdg/cache XDG_STATE_HOME=$prefix/xdg/state
	export JDTLS_JVM_ARGS="-Dmaven.repo.local=$prefix/m2/repository -Djava.util.prefs.userRoot=$prefix/java-prefs"
	export GOPATH=$prefix/gopath GOMODCACHE=$prefix/gopath/pkg/mod GOCACHE=$prefix/gocache
	export GOFLAGS=-modcacherw GOTOOLCHAIN=local
	export CARGO_HOME=$prefix/cargo
	export npm_config_cache=$prefix/npm-cache npm_config_update_notifier=false
	export JAVA_HOME=$prefix/$jdk_release
	export RUST_SRC_PATH=$prefix/rust-src-$rust_src_version/rust-src/lib/rustlib/src/rust/library
	export HUYANG_HEADLESS_INIT=$prefix/nvim-init.lua
	export LANGPROBE_LSPCONFIG=$prefix/nvim-lspconfig
	export PATH=$prefix/bin:$prefix/npm/node_modules/.bin:$JAVA_HOME/bin:$PATH
	# Inherited probe overrides could select a stale binary or reuse existing
	# sockets and state; the probe's private defaults must always apply.
	unset HUYANG_LANGPROBE HUYANG_LANGPROBE_STRICT HUYANG_LANGPROBE_LANGUAGES HUYANG_LANGPROBE_SOURCE
	unset HUYANG_LANGPROBE_BINARY HUYANG_LANGPROBE_SOCKET HUYANG_LANGPROBE_STATE_DIR HUYANG_LANGPROBE_OUTPUT
	unset HUYANG_SOCKET HUYANG_STATE_DIR AGENT99_HEADLESS_INIT
	mkdir -p "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_CACHE_HOME" "$XDG_STATE_HOME" "$CARGO_HOME" "$prefix/bin"
}

sha256_of() { sha256sum "$1" | cut -d' ' -f1; }

# fetch NAME SHA256 URL... downloads NAME into $downloads, trying each URL in
# turn until one yields the pinned digest. A cached file is reused only when
# its digest still matches.
fetch() {
	local name=$1 want=$2 url got errors=""
	shift 2
	mkdir -p "$downloads"
	if [ -f "$downloads/$name" ] && [ "$(sha256_of "$downloads/$name")" = "$want" ]; then
		return 0
	fi
	for url in "$@"; do
		rm -f "$downloads/$name.part"
		if ! curl -fsSL --retry 3 --retry-delay 5 --connect-timeout 30 -o "$downloads/$name.part" "$url" 2>"$downloads/$name.err"; then
			errors="$errors; $url: $(tr '\n' ' ' <"$downloads/$name.err")"
			continue
		fi
		got=$(sha256_of "$downloads/$name.part")
		if [ "$got" != "$want" ]; then
			errors="$errors; $url: sha256 $got, pinned $want"
			continue
		fi
		mv "$downloads/$name.part" "$downloads/$name"
		rm -f "$downloads/$name.err"
		return 0
	done
	die "download $name${errors}"
}

# stamp NAME runs the rest of the step only once per prefix.
stamped() { [ -f "$prefix/.stamp-$1" ]; }
stamp() { touch "$prefix/.stamp-$1"; }

setup_nvim() {
	local key=nvim-$nvim_version
	stamped "$key" && return 0
	fetch "nvim-$nvim_version-linux-x86_64.tar.gz" "$nvim_sha256" \
		"https://github.com/neovim/neovim/releases/download/v$nvim_version/nvim-linux-x86_64.tar.gz"
	rm -rf "$prefix/nvim-$nvim_version"
	mkdir -p "$prefix/nvim-$nvim_version"
	tar --no-same-owner -xzf "$downloads/nvim-$nvim_version-linux-x86_64.tar.gz" -C "$prefix/nvim-$nvim_version" --strip-components=1
	ln -sfn "$prefix/nvim-$nvim_version/bin/nvim" "$prefix/bin/nvim"
	stamp "$key"
}

setup_lspconfig() {
	local key=lspconfig-$lspconfig_commit dir=$prefix/nvim-lspconfig
	stamped "$key" && return 0
	rm -rf "$dir"
	# A fetch by commit is verified by the object ID itself; the archive is
	# the fallback when the Git transport is unavailable.
	if git init -q "$dir" &&
		git -C "$dir" fetch -q --depth 1 https://github.com/neovim/nvim-lspconfig "$lspconfig_commit" &&
		git -C "$dir" -c advice.detachedHead=false checkout -q FETCH_HEAD &&
		[ "$(git -C "$dir" rev-parse HEAD)" = "$lspconfig_commit" ]; then
		:
	else
		log "git fetch of nvim-lspconfig failed; using the pinned archive"
		rm -rf "$dir"
		fetch "nvim-lspconfig-$lspconfig_commit.tar.gz" "$lspconfig_sha256" \
			"https://github.com/neovim/nvim-lspconfig/archive/$lspconfig_commit.tar.gz"
		mkdir -p "$dir"
		tar --no-same-owner -xzf "$downloads/nvim-lspconfig-$lspconfig_commit.tar.gz" -C "$dir" --strip-components=1
	fi
	for server in gopls rust_analyzer pyright ts_ls jdtls clangd bashls; do
		[ -f "$dir/lsp/$server.lua" ] || die "nvim-lspconfig $lspconfig_commit has no lsp/$server.lua"
	done
	stamp "$key"
}

# The provider runs Neovim with --clean -u HUYANG_HEADLESS_INIT. This init
# stands in for the user configuration of the earlier probe, where Mason and
# mason-lspconfig enabled the same seven configurations.
write_nvim_init() {
	cat >"$prefix/nvim-init.lua" <<EOF
-- Generated by tests/langprobe-chaldea.sh; job-local, not a user config.
vim.opt.runtimepath:prepend("$prefix/nvim-lspconfig")
vim.lsp.enable({ "gopls", "rust_analyzer", "pyright", "ts_ls", "jdtls", "clangd", "bashls" })
EOF
}

setup_gopls() {
	local key=gopls-$gopls_version
	stamped "$key" && return 0
	command -v go >/dev/null || die "go is not on PATH; gopls cannot be built"
	GOBIN=$prefix/bin go install "golang.org/x/tools/gopls@$gopls_version" ||
		die "go install golang.org/x/tools/gopls@$gopls_version"
	stamp "$key"
}

setup_npm() {
	local key
	key=npm-$(printf '%s\n' "${npm_packages[@]}" | sha256sum | cut -c1-16)
	stamped "$key" && return 0
	command -v npm >/dev/null || die "npm is not on PATH; the Node language servers cannot be installed"
	mkdir -p "$prefix/npm"
	printf '{"name":"langprobe-servers","private":true}\n' >"$prefix/npm/package.json"
	# --before freezes transitive resolution at the pin date; npm checks each
	# tarball against the registry integrity hash. No install scripts run.
	npm install --prefix "$prefix/npm" --save-exact --ignore-scripts --no-audit --no-fund \
		--before="$npm_before" "${npm_packages[@]}" >&2 ||
		die "npm install ${npm_packages[*]}"
	stamp "$key"
}

setup_rust_analyzer() {
	local key=rust-analyzer-$rust_analyzer_version
	stamped "$key" && return 0
	fetch "rust-analyzer-$rust_analyzer_version-x86_64-unknown-linux-gnu.gz" "$rust_analyzer_sha256" \
		"https://github.com/rust-lang/rust-analyzer/releases/download/$rust_analyzer_version/rust-analyzer-x86_64-unknown-linux-gnu.gz"
	gzip -dc "$downloads/rust-analyzer-$rust_analyzer_version-x86_64-unknown-linux-gnu.gz" >"$prefix/bin/rust-analyzer"
	chmod 0755 "$prefix/bin/rust-analyzer"
	stamp "$key"
}

# rust-analyzer needs the standard library sources; the job toolchain has no
# rust-src component, so the matching component is unpacked here and named
# through RUST_SRC_PATH. A toolchain other than the pin uses the digest that
# static.rust-lang.org publishes for that version, and says so.
setup_rust_src() {
	command -v rustc >/dev/null || die "rustc is not on PATH; rust-analyzer needs a toolchain"
	local version want
	version=$(rustc --version | cut -d' ' -f2)
	if [ "$version" != "$rust_src_version" ]; then
		log "rustc is $version, not the pinned $rust_src_version; using the published rust-src digest for $version"
		want=$(curl -fsSL "https://static.rust-lang.org/dist/rust-src-$version.tar.xz.sha256" | cut -d' ' -f1) ||
			die "no published rust-src digest for rustc $version"
		printf 'rust-src %s digest from static.rust-lang.org (not pinned): %s\n' "$version" "$want" >>"$out/manifest.txt"
		rust_src_version=$version
		export RUST_SRC_PATH=$prefix/rust-src-$rust_src_version/rust-src/lib/rustlib/src/rust/library
	else
		want=$rust_src_sha256
	fi
	local key=rust-src-$rust_src_version
	stamped "$key" && return 0
	fetch "rust-src-$rust_src_version.tar.xz" "$want" "https://static.rust-lang.org/dist/rust-src-$rust_src_version.tar.xz"
	rm -rf "$prefix/rust-src-$rust_src_version"
	mkdir -p "$prefix/rust-src-$rust_src_version"
	tar --no-same-owner -xJf "$downloads/rust-src-$rust_src_version.tar.xz" -C "$prefix/rust-src-$rust_src_version" --strip-components=1
	[ -d "$RUST_SRC_PATH/core" ] || die "rust-src $rust_src_version did not unpack a library/core under $RUST_SRC_PATH"
	stamp "$key"
}

setup_clangd() {
	local key=clangd-$clangd_version
	stamped "$key" && return 0
	command -v python3 >/dev/null || die "python3 is needed to unpack the clangd zip (no unzip in jobs)"
	fetch "clangd-linux-$clangd_version.zip" "$clangd_sha256" \
		"https://github.com/clangd/clangd/releases/download/$clangd_version/clangd-linux-$clangd_version.zip"
	rm -rf "$prefix/clangd_$clangd_version"
	# zipfile drops permission bits, so the binary is made executable after.
	python3 -I -m zipfile -e "$downloads/clangd-linux-$clangd_version.zip" "$prefix"
	chmod 0755 "$prefix/clangd_$clangd_version/bin/clangd"
	ln -sfn "$prefix/clangd_$clangd_version/bin/clangd" "$prefix/bin/clangd"
	stamp "$key"
}

setup_shellcheck() {
	local key=shellcheck-$shellcheck_version
	stamped "$key" && return 0
	fetch "shellcheck-$shellcheck_version.linux.x86_64.tar.xz" "$shellcheck_sha256" \
		"https://github.com/koalaman/shellcheck/releases/download/$shellcheck_version/shellcheck-$shellcheck_version.linux.x86_64.tar.xz"
	tar --no-same-owner -xJf "$downloads/shellcheck-$shellcheck_version.linux.x86_64.tar.xz" -C "$prefix"
	ln -sfn "$prefix/shellcheck-$shellcheck_version/shellcheck" "$prefix/bin/shellcheck"
	stamp "$key"
}

setup_jdk() {
	local key=$jdk_release
	stamped "$key" && return 0
	fetch "$jdk_file" "$jdk_sha256" \
		"https://github.com/adoptium/temurin21-binaries/releases/download/${jdk_release/+/%2B}/$jdk_file" \
		"https://api.adoptium.net/v3/binary/version/${jdk_release/+/%2B}/linux/x64/jdk/hotspot/normal/eclipse"
	rm -rf "${prefix:?}/$jdk_release"
	tar --no-same-owner -xzf "$downloads/$jdk_file" -C "$prefix"
	[ -x "$prefix/$jdk_release/bin/java" ] || die "$jdk_file did not unpack $jdk_release/bin/java"
	stamp "$key"
}

setup_jdtls() {
	local key=$jdtls_file
	stamped "$key" && return 0
	fetch "$jdtls_file" "$jdtls_sha256" \
		"https://download.eclipse.org/jdtls/snapshots/$jdtls_file" \
		"https://download.eclipse.org/jdtls/milestones/1.60.0/$jdtls_file"
	rm -rf "$prefix/jdtls"
	mkdir -p "$prefix/jdtls"
	tar --no-same-owner -xzf "$downloads/$jdtls_file" -C "$prefix/jdtls"
	chmod 0755 "$prefix/jdtls/bin/jdtls"
	ln -sfn "$prefix/jdtls/bin/jdtls" "$prefix/bin/jdtls"
	stamp "$key"
}

write_manifest() {
	cat >>"$out/manifest.txt" <<EOF
neovim $nvim_version sha256 $nvim_sha256
nvim-lspconfig $lspconfig_commit (archive sha256 $lspconfig_sha256)
gopls $gopls_version (go install, go.sum and sum.golang.org)
rust-analyzer $rust_analyzer_version sha256 $rust_analyzer_sha256
rust-src $rust_src_version sha256 $rust_src_sha256
clangd $clangd_version sha256 $clangd_sha256
shellcheck $shellcheck_version sha256 $shellcheck_sha256
temurin $jdk_release sha256 $jdk_sha256
jdtls $jdtls_file sha256 $jdtls_sha256
npm --before=$npm_before ${npm_packages[*]} (lock: npm-package-lock.json)
EOF
}

setup() {
	setup_nvim
	setup_lspconfig
	write_nvim_init
	setup_gopls
	setup_npm
	setup_rust_analyzer
	setup_rust_src
	setup_clangd
	setup_shellcheck
	setup_jdk
	setup_jdtls
	cp "$prefix/npm/package-lock.json" "$out/npm-package-lock.json"
	du -sh "$downloads" "$prefix" 2>/dev/null | sed 's/^/size /' >>"$out/manifest.txt" || true
}

# version NAME COMMAND... records one tool's version line, or a failure.
version() {
	local name=$1 line
	shift
	if ! command -v "$1" >/dev/null; then
		printf 'MISSING %s (%s)\n' "$name" "$1" >>"$out/versions.txt"
		return 1
	fi
	if ! line=$(timeout 60 "$@" 2>&1); then
		printf 'BROKEN %s: %s: %s\n' "$name" "$*" "$(printf '%s' "$line" | head -3 | tr '\n' ' ')" >>"$out/versions.txt"
		return 1
	fi
	# The line naming the version, else the first non-empty line.
	line=$(printf '%s\n' "$line" | grep -i -m1 'version' || printf '%s\n' "$line" | grep -m1 .)
	printf '%s: %s (%s)\n' "$name" "$line" "$(command -v "$1")" >>"$out/versions.txt"
}

check_versions() {
	local bad=0
	: >"$out/versions.txt"
	version go go version || bad=1
	version git git --version || bad=1
	version cargo cargo --version || bad=1
	version rustc rustc --version || bad=1
	version node node --version || bad=1
	version python3 python3 --version || bad=1
	version nvim nvim --version || bad=1
	version gopls gopls version || bad=1
	version rust-analyzer rust-analyzer --version || bad=1
	version pyright-langserver pyright --version || bad=1
	command -v pyright-langserver >/dev/null || { echo 'MISSING pyright-langserver' >>"$out/versions.txt"; bad=1; }
	version typescript-language-server typescript-language-server --version || bad=1
	version typescript tsc --version || bad=1
	version bash-language-server bash-language-server --version || bad=1
	version shellcheck shellcheck --version || bad=1
	version clangd clangd --version || bad=1
	version java java -version || bad=1
	if command -v jdtls >/dev/null && compgen -G "$prefix/jdtls/plugins/org.eclipse.equinox.launcher_*.jar" >/dev/null; then
		printf 'jdtls: %s (%s)\n' "$jdtls_file" "$(command -v jdtls)" >>"$out/versions.txt"
	else
		echo 'MISSING jdtls launcher' >>"$out/versions.txt"
		bad=1
	fi
	cat "$out/versions.txt" >&2
	[ "$bad" = 0 ] || die "missing or broken executables: $(grep -E '^(MISSING|BROKEN)' "$out/versions.txt" | tr '\n' ';')"
	nvim --version | head -1 | grep -qx "NVIM v$nvim_version" || die "nvim on PATH is not v$nvim_version"
	java -version 2>&1 | head -1 | grep -q '"21\.' || die "java on PATH is not 21"
	case $RUST_SRC_PATH in "$prefix"/*) [ -d "$RUST_SRC_PATH/core" ] || die "RUST_SRC_PATH has no core" ;; esac
}

# A headless Neovim with the generated init opens one broken file per
# language and waits for the server to publish the expected error. This
# proves the servers, runtimes and lspconfig wiring before any Huyang code
# runs, so a failure here is a prerequisite problem, not a probe verdict.
smoke() {
	local dir=$prefix/smoke
	rm -rf "$dir"
	mkdir -p "$dir/fixtures"
	cat >"$dir/smoke.lua" <<'LUA'
local init, root, report = arg[1], arg[2], arg[3]
dofile(init)
vim.cmd("filetype on")
local wanted = {}
for name in (os.getenv("LANGPROBE_SMOKE_LANGUAGES") or ""):gmatch("[^,]+") do wanted[name] = true end
local pom = "<project><modelVersion>4.0.0</modelVersion><groupId>probe</groupId><artifactId>probe</artifactId><version>1</version><properties><maven.compiler.source>21</maven.compiler.source><maven.compiler.target>21</maven.compiler.target></properties></project>\n"
local cases = {
  { name = "go", server = "gopls", path = "main.go", line = 2, files = {
    ["main.go"] = "package main\nfunc add(a,b int) int { return \"bad\" }\nfunc main() {}\n// hi, \n",
    ["go.mod"] = "module example.com/probe\n\ngo 1.25\n" } },
  { name = "rust", server = "rust_analyzer", path = "src/main.rs", line = 1, wait = 240, files = {
    ["src/main.rs"] = "fn add(a:i32,b:i32)->i32 { \"bad\" }\nfn main(){println!(\"{}\",add(1,2));}\n// hi, \n",
    ["Cargo.toml"] = "[package]\nname=\"probe\"\nversion=\"0.1.0\"\nedition=\"2021\"\n" } },
  { name = "python", server = "pyright", path = "main.py", line = 2, files = {
    ["main.py"] = "def add(a: int, b: int) -> int:\n    return \"bad\"\n# hi, \n" } },
  { name = "typescript", server = "ts_ls", path = "src/main.ts", line = 1, files = {
    ["src/main.ts"] = "export function add(a:number,b:number):number { return \"bad\"; }\n// hi, \n",
    ["tsconfig.json"] = "{\"compilerOptions\":{\"strict\":true}}\n",
    ["package.json"] = "{\"name\":\"probe\",\"version\":\"1.0.0\"}\n" } },
  { name = "javascript", server = "ts_ls", path = "main.js", line = 3, files = {
    ["main.js"] = "// @ts-check\n/** @param {number} a @param {number} b @returns {number} */\nfunction add(a,b){return \"bad\";}\n// hi, \n",
    ["package.json"] = "{\"name\":\"probe\",\"version\":\"1.0.0\"}\n" } },
  { name = "java", server = "jdtls", path = "src/main/java/probe/Main.java", line = 2, wait = 300, files = {
    ["src/main/java/probe/Main.java"] = "package probe;\npublic class Main { static int add(int a,int b){return \"bad\";} }\n// hi, \n",
    ["pom.xml"] = pom } },
  { name = "cpp", server = "clangd", path = "main.cpp", line = 1, files = {
    ["main.cpp"] = "int add(int a,int b){return missing_symbol;}\nint main(){return add(1,2);}\n// hi, \n",
    ["compile_flags.txt"] = "-std=c++17\n" } },
  { name = "c", server = "clangd", path = "main.c", line = 1, files = {
    ["main.c"] = "int add(int a,int b){return missing_symbol;}\nint main(void){return add(1,2);}\n// hi, \n" } },
  { name = "bash", server = "bashls", path = "main.sh", line = 3, files = {
    ["main.sh"] = "#!/usr/bin/env bash\nadd() {\n  echo $undefined_one\n}\nadd 1 2\n# hi, \n" } },
}
local function seconds(since) return (vim.uv.hrtime() - since) / 1e9 end
local results, failed = {}, 0
for _, c in ipairs(cases) do
  if next(wanted) == nil or wanted[c.name] then
    local dir = root .. "/" .. c.name
    for path, body in pairs(c.files) do
      local file = dir .. "/" .. path
      vim.fn.mkdir(vim.fs.dirname(file), "p")
      local handle = assert(io.open(file, "w"))
      handle:write(body)
      handle:close()
    end
    vim.system({ "git", "init", "-q", dir }):wait()
    local r = { language = c.name, config = c.server, expected_line = c.line, diagnostics = {} }
    local started = vim.uv.hrtime()
    vim.cmd.edit(vim.fn.fnameescape(dir .. "/" .. c.path))
    local buf = vim.api.nvim_get_current_buf()
    local client
    vim.wait((c.wait or 90) * 1000, function()
      for _, cl in ipairs(vim.lsp.get_clients({ bufnr = buf, name = c.server })) do
        if cl.initialized then client = cl return true end
      end
      return false
    end, 200)
    if client then
      r.attach_s = seconds(started)
      r.server_info = client.server_info
      -- A save makes rust-analyzer run cargo check and jdtls build.
      vim.cmd("silent write")
      local warn = { severity = { min = vim.diagnostic.severity.WARN } }
      if vim.wait((c.wait or 90) * 1000, function() return #vim.diagnostic.get(buf, warn) > 0 end, 200) then
        r.diagnostic_s = seconds(started)
        vim.wait(3000)
      end
      local on_line, off_line = 0, 0
      for _, d in ipairs(vim.diagnostic.get(buf, warn)) do
        r.diagnostics[#r.diagnostics + 1] = { line = d.lnum + 1, severity = d.severity, source = d.source, code = d.code, message = d.message }
        if d.lnum + 1 == c.line then on_line = on_line + 1 else off_line = off_line + 1 end
      end
      if on_line == 0 then
        r.reason = "no warning or error on line " .. c.line
      elseif off_line > 0 then
        r.reason = off_line .. " unexpected diagnostics off line " .. c.line
      end
      client:stop(true)
    else
      r.reason = c.server .. " did not attach within " .. (c.wait or 90) .. " s"
    end
    r.ok = r.reason == nil
    if not r.ok then failed = failed + 1 end
    results[#results + 1] = r
    local first = r.diagnostics[1]
    io.stdout:write(string.format("smoke %-10s %-4s attach=%.1fs diag=%s %s\n", c.name, r.ok and "ok" or "FAIL",
      r.attach_s or -1, r.diagnostic_s and string.format("%.1fs", r.diagnostic_s) or "none",
      r.reason or (first and ("L" .. first.line .. " " .. tostring(first.message):gsub("\n", " ")) or "")))
    vim.cmd("silent! bwipeout!")
  end
end
local handle = assert(io.open(report, "w"))
handle:write(vim.json.encode(results))
handle:close()
os.exit(failed == 0 and 0 or 1)
LUA
	LANGPROBE_SMOKE_LANGUAGES=${LANGPROBE_LANGUAGES:-} timeout 1800 nvim --clean --headless \
		-l "$dir/smoke.lua" "$HUYANG_HEADLESS_INIT" "$dir/fixtures" "$out/lsp-smoke.json" 2>&1 |
		tee "$out/lsp-smoke.txt" >&2
	[ "${PIPESTATUS[0]}" = 0 ] || die "language-server smoke: $(grep -c FAIL "$out/lsp-smoke.txt") failing, see lsp-smoke.txt"
}

probe() {
	local languages=${LANGPROBE_LANGUAGES:-$languages_all} rc lang status verdict=PASS strict=1
	local output=$out/langprobe
	rm -rf "$output"
	mkdir -p "$output"
	# Strict mode asserts every intermediate reply and refuses a language
	# filter, so a subset runs without it and is only ever partial.
	if [ "$languages" != "$languages_all" ]; then
		export HUYANG_LANGPROBE_LANGUAGES=$languages
		strict=
		log "running TestLangprobeABCD for $languages (partial, not strict, never acceptance)"
	else
		unset HUYANG_LANGPROBE_LANGUAGES
		log "running TestLangprobeABCD for all nine languages (strict)"
	fi
	set +e
	# A build without strict support ignores the variable, so a baseline run
	# is weaker and must be labelled as such.
	(cd "$repo/cmd/huyang" && { [ -z "$strict" ] || export HUYANG_LANGPROBE_STRICT=1; } &&
		HUYANG_FRICTION=0 HUYANG_LANGPROBE=1 HUYANG_LANGPROBE_OUTPUT=$output \
		go test -run '^TestLangprobeABCD$' -count=1 -v -timeout "${LANGPROBE_TIMEOUT:-45m}" .) 2>&1 |
		tee "$out/go-test.txt" >&2
	rc=${PIPESTATUS[0]}
	set -e
	: >"$out/languages.txt"
	for lang in ${languages//,/ }; do
		if grep -q -- "--- PASS: TestLangprobeABCD/$lang " "$out/go-test.txt"; then
			status=PASS
		elif grep -q -- "--- SKIP: TestLangprobeABCD/$lang " "$out/go-test.txt"; then
			status=SKIP
		elif grep -q -- "--- FAIL: TestLangprobeABCD/$lang " "$out/go-test.txt"; then
			status=FAIL
		else
			status=MISSING
		fi
		[ "$status" = PASS ] || verdict=FAIL
		printf '%s %s\n' "$lang" "$status" >>"$out/languages.txt"
	done
	grep -q -- '--- SKIP: TestLangprobeABCD ' "$out/go-test.txt" && verdict=FAIL
	[ "$rc" = 0 ] || verdict=FAIL
	cat "$out/languages.txt" >&2
	printf 'probe %s (go test exit %s)\n' "$verdict" "$rc" >>"$out/summary.txt"
	sed 's/^/language /' "$out/languages.txt" >>"$out/summary.txt"
	[ "$verdict" = PASS ] || die "TestLangprobeABCD: $(grep -v ' PASS$' "$out/languages.txt" | tr '\n' ' ')(go test exit $rc)"
}

# gotest NAME ARGS... runs go test from the repository root in the
# provisioned environment, for the full suite and the race suite.
gotest() {
	local name=$1 rc
	shift
	log "go test ($name): $*"
	set +e
	(cd "$repo" && HUYANG_FRICTION=0 go test "$@") 2>&1 | tee "$out/go-test.txt" >&2
	rc=${PIPESTATUS[0]}
	set -e
	printf '%s: go test %s (exit %s)\n' "$name" "$*" "$rc" >>"$out/summary.txt"
	grep -E '^(ok|FAIL|---)' "$out/go-test.txt" | grep -vE '^--- (PASS|SKIP)' | sed 's/^/  /' >>"$out/summary.txt" || true
	[ "$rc" = 0 ] || die "go test ($name) exit $rc: $(grep -E '^(FAIL|--- FAIL)' "$out/go-test.txt" | head -20 | tr '\n' ';')"
}

# Always leave a summary and a bounded report behind, also after a failure,
# so the Chaldea artifacts exist. Probe workspaces lose their Git metadata
# and cargo target directories to keep the report small.
finish() {
	local rc=$?
	set +e
	if [ "$rc" = 0 ]; then
		printf 'result OK (%s)\n' "$mode" >>"$out/summary.txt"
	else
		printf 'result FAILED (%s, exit %s)\n' "$mode" "$rc" >>"$out/summary.txt"
		sed 's/^/  /' "$out/failures.txt" >>"$out/summary.txt" 2>/dev/null
	fi
	tar --no-same-owner -czf "$out/langprobe-report.tar.gz" -C "$out" \
		--exclude=langprobe-report.tar.gz --exclude=.git --exclude=target --exclude=node_modules . 2>/dev/null
	cat "$out/summary.txt" >&2
	exit "$rc"
}

begin() {
	rm -rf "${out:?}"
	mkdir -p "$out"
	: >"$out/manifest.txt"
	{
		printf 'mode %s\n' "$mode"
		printf 'label %s\n' "${LANGPROBE_LABEL:-unlabelled}"
		printf 'host %s, started %s\n' "$(uname -n)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		printf 'languages %s\n' "${LANGPROBE_LANGUAGES:-$languages_all}"
		if [ -n "${LANGPROBE_LANGUAGES:-}" ] && [ "$LANGPROBE_LANGUAGES" != "$languages_all" ]; then
			printf 'PARTIAL: a language subset runs without strict mode and never counts as acceptance\n'
		fi
	} >"$out/summary.txt"
	write_manifest
	trap finish EXIT
}

submit() {
	local inner=${1:-probe} host=${LANGPROBE_HOST:-agent-a} rc
	local -a env_args=(HUYANG_FRICTION=0)
	local name
	[ $# -gt 0 ] && shift
	for name in LANGPROBE_LABEL LANGPROBE_LANGUAGES LANGPROBE_TIMEOUT LANGPROBE_SKIP_SMOKE LANGPROBE_TMPDIR; do
		[ -n "${!name:-}" ] && env_args+=("$name=${!name}")
	done
	case $inner in setup | check | probe | suite | race | run) ;; *) die "submit takes setup, check, probe, suite, race or run" ;; esac
	command -v chaldea >/dev/null || die "chaldea is not on PATH"
	cd "$repo"
	# One dispatch only. A busy refusal is reported so the caller can register
	# a Chaldea wait; an uncertain outcome is never re-run, and the journal
	# entry chaldea names is the place to resolve it.
	set +e
	chaldea exec --host "$host" \
		--artifact tests/.deps/langprobe/out/summary.txt \
		--artifact tests/.deps/langprobe/out/langprobe-report.tar.gz \
		-- env "${env_args[@]}" bash tests/langprobe-chaldea.sh "$inner" "$@" 2>&1 | tee "$out/submit.log"
	rc=${PIPESTATUS[0]}
	set -e
	if [ "$rc" != 0 ] && grep -qE '^chaldea: host .* refused: .*nothing dispatched' "$out/submit.log"; then
		log "BUSY: host $host refused before dispatch, nothing ran (exit $rc); not retried"
	elif [ "$rc" != 0 ]; then
		log "chaldea exec exit $rc; if the outcome is uncertain, inspect the journal it names and do not re-run"
	fi
	return "$rc"
}

mode=${1:-}
case $mode in
setup)
	begin
	export_env
	setup
	;;
check)
	begin
	export_env
	setup
	check_versions
	smoke
	;;
probe)
	begin
	export_env
	setup
	check_versions
	[ "${LANGPROBE_SKIP_SMOKE:-}" = 1 ] || smoke
	probe
	;;
suite | race)
	shift
	begin
	export_env
	setup
	check_versions
	[ $# -gt 0 ] || set -- ./...
	if [ "$mode" = race ]; then
		gotest race -race -count=1 "$@"
	else
		gotest suite -count=1 "$@"
	fi
	;;
run)
	shift
	[ $# -gt 0 ] || die "run needs a command"
	begin
	export_env
	setup
	check_versions
	printf 'run: %s\n' "$*" >>"$out/summary.txt"
	(cd "$repo" && "$@")
	;;
env)
	export_env
	for name in TMPDIR XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME GOPATH GOMODCACHE GOCACHE GOFLAGS \
		GOTOOLCHAIN CARGO_HOME npm_config_cache JDTLS_JVM_ARGS JAVA_HOME RUST_SRC_PATH HUYANG_HEADLESS_INIT HUYANG_FRICTION PATH; do
		printf 'export %s=%q\n' "$name" "${!name}"
	done
	;;
submit)
	shift
	# The submit log lives in one task cache, never in the inherited TMPDIR.
	submit_log_dir=$(scratch_dir "$HOME/.cache/huyang-langprobe") ||
		die "$HOME/.cache/huyang-langprobe is not usable for the submit log"
	out=$submit_log_dir
	submit "$@"
	;;
selftest)
	# Static guards only: no downloads, no jobs, nothing under /tmp is created.
	fails=0
	for path in /tmp /tmp/ /tmp/langprobe-selftest-never-created; do
		if scratch_dir "$path" >/dev/null 2>&1; then
			log "selftest: $path was accepted"
			fails=1
		fi
	done
	ln -sfn /tmp "$prefix/selftest-link"
	if scratch_dir "$prefix/selftest-link" >/dev/null 2>&1; then
		log "selftest: a symbolic link to /tmp was accepted"
		fails=1
	fi
	# Missing children below a link to /tmp must be refused before mkdir runs.
	child=langprobe-selftest-$$-new-scratch
	if [ -e "/tmp/$child" ]; then
		log "selftest: /tmp/$child already exists; cannot prove it is not created"
		fails=1
	fi
	for path in "$prefix/selftest-link/$child" "$prefix/selftest-link/$child/deeper"; do
		if scratch_dir "$path" >/dev/null 2>&1; then
			log "selftest: $path (below a link to /tmp) was accepted"
			fails=1
		fi
	done
	if (LANGPROBE_TMPDIR=$prefix/selftest-link/$child export_env) >/dev/null 2>&1; then
		log "selftest: LANGPROBE_TMPDIR below a link to /tmp was accepted"
		fails=1
	fi
	if [ -e "/tmp/$child" ]; then
		log "selftest: /tmp/$child was created"
		rmdir "/tmp/$child/deeper" "/tmp/$child" 2>/dev/null
		fails=1
	fi
	rm -f "$prefix/selftest-link"
	if (LANGPROBE_TMPDIR=/tmp/langprobe-selftest-never-created export_env) >/dev/null 2>&1; then
		log "selftest: LANGPROBE_TMPDIR under /tmp was accepted"
		fails=1
	fi
	left=$(
		export HUYANG_LANGPROBE_BINARY=/stale HUYANG_LANGPROBE_SOCKET=/stale HUYANG_LANGPROBE_STATE_DIR=/stale
		export HUYANG_LANGPROBE_OUTPUT=/stale HUYANG_SOCKET=/stale HUYANG_STATE_DIR=/stale TMPDIR=/tmp
		export_env >/dev/null
		env | grep -E '^(HUYANG_LANGPROBE[A-Z_]*|HUYANG_SOCKET|HUYANG_STATE_DIR)=' || true
		case $TMPDIR/ in /tmp/*) echo "TMPDIR=$TMPDIR" ;; esac
	) || left="export_env failed"
	if [ -n "$left" ]; then
		log "selftest: export_env left $(printf '%s' "$left" | tr '\n' ' ')"
		fails=1
	fi
	[ "$fails" = 0 ] || exit 1
	log "selftest: guards ok"
	;;
*)
	awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
	exit 2
	;;
esac
