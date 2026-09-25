param(
    [Parameter(Mandatory = $true)]
    [string[]]$WondercardPath,

    [Parameter(Mandatory = $true)]
    [string[]]$ProfileRoot
)

$ErrorActionPreference = 'Stop'
$cardSize = 0x2C8
$cards = [System.Collections.Generic.List[object]]::new()

function Get-Wc9Checksum([byte[]]$Bytes) {
    $crc = 0xFFFF
    for ($offset = 0; $offset -lt $Bytes.Length; $offset++) {
        $octet = if ($offset -eq 0x2C4 -or $offset -eq 0x2C5) { 0 } else { [int]$Bytes[$offset] }
        $crc = $crc -bxor ($octet -shl 8)
        for ($bit = 0; $bit -lt 8; $bit++) {
            if (($crc -band 0x8000) -ne 0) {
                $crc = ((($crc -shl 1) -bxor 0x1021) -band 0xFFFF)
            }
            else {
                $crc = (($crc -shl 1) -band 0xFFFF)
            }
        }
    }
    return [uint16]$crc
}

foreach ($path in $WondercardPath) {
    $resolved = (Resolve-Path -LiteralPath $path).Path
    $data = [System.IO.File]::ReadAllBytes($resolved)
    if ($data.Length -ne $cardSize) {
        throw "Wondercard '$resolved' has $($data.Length) bytes; a Violet WC9 must have $cardSize bytes."
    }
    if ($data[0x0F] -ne 0 -or $data[0x2C0] -ne 0) {
        throw "Wondercard '$resolved' does not match the Scarlet/Violet WC9 markers."
    }
    if ([BitConverter]::ToUInt16($data, 0x2C4) -ne (Get-Wc9Checksum $data)) {
        throw "Wondercard '$resolved' has an invalid WC9 checksum."
    }

    $id = [BitConverter]::ToUInt16($data, 0x08)
    $cards.Add([pscustomobject]@{ Id = $id; Path = $resolved; Data = $data })
}

$duplicates = $cards | Group-Object Id | Where-Object Count -gt 1
if ($duplicates) {
    $ids = ($duplicates.Name | Sort-Object) -join ', '
    throw "Duplicate WC9 IDs are not allowed in one catalog: $ids"
}

$ordered = @($cards | Sort-Object Id)
$catalog = [byte[]]::new($ordered.Count * $cardSize)
for ($i = 0; $i -lt $ordered.Count; $i++) {
    [Array]::Copy($ordered[$i].Data, 0, $catalog, $i * $cardSize, $cardSize)
}

$sha256 = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($catalog))
foreach ($root in $ProfileRoot) {
    $resolvedRoot = (Resolve-Path -LiteralPath $root).Path
    $directory = Join-Path $resolvedRoot 'bcat-seed\normal'
    [System.IO.Directory]::CreateDirectory($directory) | Out-Null

    $destination = Join-Path $directory 'distribution_internet'
    $temporary = Join-Path $directory ('.distribution_internet.' + [Guid]::NewGuid().ToString('N') + '.tmp')
    try {
        [System.IO.File]::WriteAllBytes($temporary, $catalog)
        Move-Item -LiteralPath $temporary -Destination $destination -Force
    }
    finally {
        if (Test-Path -LiteralPath $temporary) {
            Remove-Item -LiteralPath $temporary -Force
        }
    }

    Write-Output "Installed $($ordered.Count) WC9 card(s) at '$destination' (SHA-256 $sha256)."
}

Write-Output ('WCIDs: ' + (($ordered.Id | ForEach-Object { '{0:D4}' -f $_ }) -join ', '))
