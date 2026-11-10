package gitidentity

// networkScopeScript runs only inside the isolated POSIX Git wrapper. Resolve
// named remotes to URLs for shell/plugin commands; never inspect saved tokens.
// export assignments keep credentials out of the child command's arguments.
const networkScopeScript = `
scope_url() {
 case "$1" in https://*|http://*) ;; *) return ;; esac
 case "$1" in *\?*|*\#*) return ;; esac
 export "GIT_CONFIG_KEY_${scope_count}=http.$1.extraHeader"
 export "GIT_CONFIG_VALUE_${scope_count}="
 scope_count=$((scope_count + 1))
 case "$1" in
  "https://${OV_GIT_AUTH_HOST}/"*)
   if [ -n "${OV_GIT_AUTH_HEADER-}" ]; then
    export "GIT_CONFIG_KEY_${scope_count}=http.$1.extraHeader"
    export "GIT_CONFIG_VALUE_${scope_count}=$OV_GIT_AUTH_HEADER"
    scope_count=$((scope_count + 1))
   fi ;;
 esac
}
network=0
for argument do
 case "$argument" in clone|fetch|push|pull|ls-remote|submodule) network=1 ;; esac
done
if [ "$network" = 1 ]; then
 scope_count=${GIT_CONFIG_COUNT:-0}
 case "$scope_count" in *[!0-9]*) exit 1 ;; esac
 repo_dir=$PWD
 directory_next=0
 for argument do
  if [ "$directory_next" = 1 ]; then
   repo_dir=$(cd "$repo_dir" && cd "$argument" && pwd) || exit 1
   directory_next=0
  elif [ "$argument" = -C ]; then
   directory_next=1
  fi
  scope_url "$argument"
 done
 # Remotes are single-line names and URLs. Keep each URL intact (including
 # spaces); use only newline as the field separator while reading Git output.
 old_ifs=$IFS
 IFS='
'
 for remote in $("$real_git" -C "$repo_dir" remote 2>/dev/null); do
  for target in $("$real_git" -C "$repo_dir" remote get-url --all "$remote" 2>/dev/null) $("$real_git" -C "$repo_dir" remote get-url --push --all "$remote" 2>/dev/null); do
   scope_url "$target"
  done
 done
 IFS=$old_ifs
 export GIT_CONFIG_COUNT=$scope_count
fi
`

// windowsNetworkScopeScript mirrors the POSIX wrapper using process-local
// environment settings. No token is written to disk or passed as an argument.
const windowsNetworkScopeScript = `
$gitArgs = @($args)
function Add-Config([string] $setting) {
 $script:configParameters += " '" + $setting.Replace("'", "'\''") + "'"
}
function Scope-Url([string] $target) {
 if (-not ($target.StartsWith('https://', [StringComparison]::Ordinal) -or $target.StartsWith('http://', [StringComparison]::Ordinal))) { return }
 if ($target.Contains('?') -or $target.Contains('#')) { return }
 Add-Config "http.$target.extraHeader="
 if ($target.StartsWith("https://$env:OV_GIT_AUTH_HOST/", [StringComparison]::Ordinal) -and $env:OV_GIT_AUTH_HEADER) {
  Add-Config "http.$target.extraHeader=$env:OV_GIT_AUTH_HEADER"
 }
}
$network = $false
foreach ($argument in $gitArgs) {
 if (@('clone', 'fetch', 'push', 'pull', 'ls-remote', 'submodule') -ccontains $argument) { $network = $true }
}
if ($network) {
 # Windows PowerShell removes empty environment values. Use Git's quoted
 # parameter format so empty header resets survive in the child process.
 $script:configParameters = $env:GIT_CONFIG_PARAMETERS
 $repoDir = (Get-Location).Path
 $directoryNext = $false
 foreach ($argument in $gitArgs) {
  if ($directoryNext) {
   if ($argument -ne '') {
    if ([IO.Path]::IsPathRooted($argument)) { $repoDir = $argument }
    else { $repoDir = [IO.Path]::GetFullPath((Join-Path $repoDir $argument)) }
   }
   $directoryNext = $false
  } elseif ($argument -ceq '-C') { $directoryNext = $true }
  Scope-Url $argument
 }
 foreach ($remote in @(& $realGit -C $repoDir remote 2>$null)) {
  foreach ($target in @(& $realGit -C $repoDir remote get-url --all $remote 2>$null)) { Scope-Url $target }
  foreach ($target in @(& $realGit -C $repoDir remote get-url --push --all $remote 2>$null)) { Scope-Url $target }
 }
 $env:GIT_CONFIG_PARAMETERS = $script:configParameters
}
& $realGit @gitArgs
exit $LASTEXITCODE
`
