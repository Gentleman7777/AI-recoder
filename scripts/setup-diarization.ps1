param([string]$Python = 'python')
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path $PSScriptRoot -Parent
$venvRoot = Join-Path $projectRoot '.venv-diarization'
& $Python -m venv $venvRoot
if ($LASTEXITCODE -ne 0) { throw 'Не удалось создать окружение Python (нужен Python 3.12 или новее).' }
$venvPython = Join-Path $venvRoot 'Scripts/python.exe'
& $venvPython -m pip install -r (Join-Path $PSScriptRoot 'diarization-requirements.txt')
if ($LASTEXITCODE -ne 0) { throw 'Не удалось установить модуль разделения голосов.' }
& $venvPython (Join-Path $PSScriptRoot 'download-speaker-models.py')
if ($LASTEXITCODE -ne 0) { throw 'Не удалось скачать модели голосов.' }
Write-Output "Готово. Добавьте в .env и перезапустите сервер:"
Write-Output 'DIARIZATION_ENABLED=true'
Write-Output "DIARIZATION_PYTHON=$venvPython"
