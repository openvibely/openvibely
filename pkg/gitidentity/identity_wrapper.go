package gitidentity

// Preserve only Git's identity config before isolating HOME and credentials.
// Pass config values, not author/committer environment overrides, so Git retains
// its normal per-role precedence. Config queries do not execute hooks or aliases.
const identityFallbackScript = `
identity_config() (
 identity_key=$1
 shift
 export GIT_CONFIG_PARAMETERS=${OV_GIT_IDENTITY_PARAMETERS-}
 export GIT_CONFIG_COUNT=0
 if [ "${OV_GIT_IDENTITY_GIT_CONFIG_GLOBAL+x}" = x ]; then
  export GIT_CONFIG_GLOBAL=$OV_GIT_IDENTITY_GIT_CONFIG_GLOBAL
 else unset GIT_CONFIG_GLOBAL; fi
 if [ "${OV_GIT_IDENTITY_GIT_CONFIG_SYSTEM+x}" = x ]; then
  export GIT_CONFIG_SYSTEM=$OV_GIT_IDENTITY_GIT_CONFIG_SYSTEM
 else unset GIT_CONFIG_SYSTEM; fi
 if [ "${OV_GIT_IDENTITY_GIT_CONFIG_NOSYSTEM+x}" = x ]; then
  export GIT_CONFIG_NOSYSTEM=$OV_GIT_IDENTITY_GIT_CONFIG_NOSYSTEM
 else unset GIT_CONFIG_NOSYSTEM; fi
 # Rotate global options to the end, then remove the original command and its
 # operands. Preserve quoting and let Git interpret -C, -c and config includes.
 identity_remaining=$#
 while [ "$identity_remaining" -gt 0 ]; do
  case "$1" in
   -C|-c|--git-dir|--work-tree|--namespace|--config-env)
    [ "$identity_remaining" -ge 2 ] || exit 1
    set -- "$@" "$1" "$2"
    shift 2
    identity_remaining=$((identity_remaining - 2)) ;;
   --git-dir=*|--work-tree=*|--namespace=*|--config-env=*|--exec-path=*|--super-prefix=*|--attr-source=*|-p|-P|--no-replace-objects|--no-lazy-fetch|--no-advice|--no-pager|--paginate|--no-optional-locks|--literal-pathspecs|--glob-pathspecs|--noglob-pathspecs|--icase-pathspecs|--bare)
    set -- "$@" "$1"
    shift
    identity_remaining=$((identity_remaining - 1)) ;;
   *) break ;;
  esac
 done
 while [ "$identity_remaining" -gt 0 ]; do
  shift
  identity_remaining=$((identity_remaining - 1))
 done
 "$real_git" "$@" config --get "$identity_key" 2>/dev/null
)
if [ -z "${GIT_AUTHOR_NAME-}" ] || [ -z "${GIT_COMMITTER_NAME-}" ] || [ -z "${GIT_AUTHOR_EMAIL-}" ] || [ -z "${GIT_COMMITTER_EMAIL-}" ]; then
 identity_count=${GIT_CONFIG_COUNT:-0}
 case "$identity_count" in *[!0-9]*) exit 1 ;; esac
 for identity_key in user.name user.email author.name author.email committer.name committer.email; do
  identity_present=0
  if identity_value=$(identity_config "$identity_key" "$@"); then identity_present=1; fi
  if [ -z "$identity_value" ]; then
   case "$identity_key" in
    user.name) identity_value='OpenVibely Bot'; identity_present=1 ;;
    user.email) identity_value=${EMAIL:-bot@openvibely.ai}; identity_present=1 ;;
   esac
  fi
  if [ "$identity_present" = 1 ]; then
   export "GIT_CONFIG_KEY_${identity_count}=$identity_key"
   export "GIT_CONFIG_VALUE_${identity_count}=$identity_value"
   identity_count=$((identity_count + 1))
  fi
 done
 export GIT_CONFIG_COUNT=$identity_count
fi
`

const windowsIdentityFallbackScript = `
function Read-Identity([string] $key, [object[]] $commandArgs) {
 $saved = @{}
 foreach ($name in @('GIT_CONFIG_PARAMETERS','GIT_CONFIG_COUNT','GIT_CONFIG_GLOBAL','GIT_CONFIG_SYSTEM','GIT_CONFIG_NOSYSTEM')) {
  $saved[$name] = [Environment]::GetEnvironmentVariable($name)
  $value = [Environment]::GetEnvironmentVariable('OV_GIT_IDENTITY_'+$name)
  if ($name -eq 'GIT_CONFIG_COUNT') { $value = '0' }
  if ($name -eq 'GIT_CONFIG_PARAMETERS') { $value = $env:OV_GIT_IDENTITY_PARAMETERS }
  [Environment]::SetEnvironmentVariable($name, $value)
 }
 try {
  $prefix = @()
  for ($n=0; $n -lt $commandArgs.Count; $n++) {
   $arg = [string]$commandArgs[$n]
   if (@('-C','-c','--git-dir','--work-tree','--namespace','--config-env') -ccontains $arg) {
    $prefix += $arg; $n++
    if ($n -ge $commandArgs.Count) { return $null }
    $prefix += $commandArgs[$n]
   } elseif ($arg -cmatch '^--(git-dir|work-tree|namespace|config-env|exec-path|super-prefix|attr-source)=' -or @('-p','-P','--no-replace-objects','--no-lazy-fetch','--no-advice','--no-pager','--paginate','--no-optional-locks','--literal-pathspecs','--glob-pathspecs','--noglob-pathspecs','--icase-pathspecs','--bare') -ccontains $arg) { $prefix += $arg }
   else { break }
  }
  $value = ((& $realGit @prefix config --get $key 2>$null) -join "` + "`n" + `")
  if ($LASTEXITCODE -eq 0) { return @{ Value = $value } }
  return $null
 } finally {
  foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name]) }
 }
}
if (-not $env:GIT_AUTHOR_NAME -or -not $env:GIT_COMMITTER_NAME -or -not $env:GIT_AUTHOR_EMAIL -or -not $env:GIT_COMMITTER_EMAIL) {
 $identityParameters = $env:GIT_CONFIG_PARAMETERS
 foreach ($key in @('user.name','user.email','author.name','author.email','committer.name','committer.email')) {
  $identity = Read-Identity $key @($args)
  if ($null -eq $identity -or [string]::IsNullOrEmpty($identity.Value)) {
   if ($key -eq 'user.name') { $identity = @{ Value = 'OpenVibely Bot' } }
   if ($key -eq 'user.email') {
    $email = $env:EMAIL
    if (-not $email) { $email = 'bot@openvibely.ai' }
    $identity = @{ Value = $email }
   }
  }
  if ($null -ne $identity) {
   $setting = $key + '=' + $identity.Value
   $identityParameters += " '" + $setting.Replace("'", "'\''") + "'"
  }
 }
 $env:GIT_CONFIG_PARAMETERS = $identityParameters
}
`
