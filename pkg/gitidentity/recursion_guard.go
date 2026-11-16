package gitidentity

// Compare wrapper contents instead of path spelling. File reads follow aliases,
// including hard links, symlinks and junctions; copied OV wrappers are rejected
// too. Check lengths first so a real git.exe is not read into memory each call.
// Keep this separate so Windows tests can exercise the guard without ever
// executing the recursive fallback if the guard regresses.
const windowsRecursionGuardScript = `$realGit = $env:OV_GIT_REAL_EXECUTABLE
if (-not $realGit) { throw 'missing Git executable' }
$targetStream = [IO.File]::OpenRead($realGit)
try { $targetLength = $targetStream.Length } finally { $targetStream.Dispose() }
foreach ($wrapperName in @('git.cmd', 'git-wrapper.ps1')) {
 $wrapperFile = Get-Item -LiteralPath (Join-Path $PSScriptRoot $wrapperName) -ErrorAction Stop
 if ($targetLength -eq $wrapperFile.Length -and
     [Convert]::ToBase64String([IO.File]::ReadAllBytes($realGit)) -ceq
     [Convert]::ToBase64String([IO.File]::ReadAllBytes($wrapperFile.FullName))) {
  [Console]::Error.WriteLine('OV Git wrapper cannot use itself as real Git')
  exit 125
 }
}
`
