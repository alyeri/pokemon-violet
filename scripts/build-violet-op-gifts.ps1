param(
    [Parameter(Mandatory = $true)]
    [string]$TemplatePath,

    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory
)

$ErrorActionPreference = 'Stop'
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

$template = [System.IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $TemplatePath).Path)
if ($template.Length -ne 0x2C8 -or $template[0x11] -ne 2) {
    throw 'The template must be a 712-byte Scarlet/Violet item WC9.'
}
if ([BitConverter]::ToUInt16($template, 0x2C4) -ne (Get-Wc9Checksum $template)) {
    throw 'The template WC9 checksum is invalid.'
}

$sets = @(
    @{ Id = 8001; Name = 'rare-items'; Items = @(@(1, 50), @(50, 500), @(1606, 100)) },
    @{ Id = 8002; Name = 'tera-shards-01'; Items = @(@(1862, 500), @(1863, 500), @(1864, 500), @(1865, 500), @(1866, 500), @(1867, 500)) },
    @{ Id = 8003; Name = 'tera-shards-02'; Items = @(@(1868, 500), @(1869, 500), @(1870, 500), @(1871, 500), @(1872, 500), @(1873, 500)) },
    @{ Id = 8004; Name = 'tera-shards-03'; Items = @(@(1874, 500), @(1875, 500), @(1876, 500), @(1877, 500), @(1878, 500), @(1879, 500)) },
    @{ Id = 8005; Name = 'stellar-tera-shards'; Items = ,@(2549, 500) }
)

[System.IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
foreach ($set in $sets) {
    if ($set.Items.Count -gt 6) { throw "Too many item slots in $($set.Name)." }
    $card = [byte[]]$template.Clone()
    [BitConverter]::GetBytes([uint16]$set.Id).CopyTo($card, 0x08)
    $card[0x0E] = 2 # Violet only.
    $card[0x15] = 3 # Localized "Item Set Gift" title.
    [Array]::Clear($card, 0x18, 6 * 4)
    for ($index = 0; $index -lt $set.Items.Count; $index++) {
        $item = $set.Items[$index]
        if ($item.Count -ne 2 -or $item[1] -lt 1 -or $item[1] -gt 999) {
            throw "Invalid item or quantity in $($set.Name)."
        }
        [BitConverter]::GetBytes([uint16]$item[0]).CopyTo($card, 0x18 + 4 * $index)
        [BitConverter]::GetBytes([uint16]$item[1]).CopyTo($card, 0x1A + 4 * $index)
    }
    [BitConverter]::GetBytes((Get-Wc9Checksum $card)).CopyTo($card, 0x2C4)
    $path = Join-Path $OutputDirectory ('{0:D4}-{1}.wc9' -f $set.Id, $set.Name)
    [System.IO.File]::WriteAllBytes($path, $card)
    Write-Output ('{0}: {1} item(s), SHA-256 {2}' -f $path, $set.Items.Count,
        [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($card)))
}
