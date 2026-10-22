param(
    [string]$OllamaURL = 'http://localhost:11434',
    [string]$Model = 'qwen3:4b',
    [ValidateSet('both','before','after')][string]$Mode = 'both'
)
# Synthetic input only. Never prints or stores the model's reasoning trace.
$fixture = 'На встрече решили выпустить обновление в пятницу. Анна проверит интерфейс до четверга, а Иван подготовит инструкцию. Следующая встреча состоится в понедельник. Дополнительные расходы не согласовали.'
$oldPrompt = 'Ты помощник для обработки аудиозаписей. Ответь только JSON-объектом с полями summary (строка на русском) и key_points (массив коротких тезисов на русском). Не выдумывай факты, которых нет в расшифровке.'
$fastPrompt = 'Сожми короткую запись в одно предложение на русском, до 20 слов. Верни только JSON с полями summary и key_points. key_points: пустой массив, если дополнительных выводов нет; иначе до 2 тезисов по 8 слов. Не повторяй резюме в тезисах. Не добавляй факты.'
$schema = @{
    type = 'object'
    properties = @{summary=@{type='string'};key_points=@{type='array';items=@{type='string'}}}
    required = @('summary','key_points')
    additionalProperties = $false
}
$results = @()
$modes = if ($Mode -eq 'both') { @('before','after') } else { @($Mode) }
foreach ($testMode in $modes) {
    $prompt = if ($testMode -eq 'before') { $oldPrompt } else { $fastPrompt }
    $payload = @{
        model=$Model;stream=$false;format=$schema;keep_alive='5m'
        messages=@(@{role='system';content=$prompt},@{role='user';content="Сделай краткое содержание и основные тезисы этой расшифровки:`n`n$fixture"})
        options=@{num_predict=1024}
    }
    # The old request is capped for the benchmark so it cannot reason indefinitely.
    if ($testMode -eq 'after') {
        $payload.think=$false; $payload.options=@{num_predict=256;temperature=0.2}
        $schema.properties.key_points.maxItems=2
    }
    $timer = [Diagnostics.Stopwatch]::StartNew()
    try {
        $body = [Text.Encoding]::UTF8.GetBytes(($payload | ConvertTo-Json -Depth 10 -Compress))
        $reply = Invoke-RestMethod -Uri "$($OllamaURL.TrimEnd('/'))/api/chat" -Method Post -ContentType 'application/json; charset=utf-8' -Body $body -TimeoutSec 180
        $valid = $false
        try { $answer=$reply.message.content | ConvertFrom-Json; $valid=($answer.summary.Trim().Length -gt 0 -and $null -ne $answer.key_points) } catch {}
        $result = [pscustomobject]@{
            mode=$testMode;seconds=[math]::Round($timer.Elapsed.TotalSeconds,2)
            load_seconds=[math]::Round($reply.load_duration/1e9,2)
            prompt_seconds=[math]::Round($reply.prompt_eval_duration/1e9,2)
            generate_seconds=[math]::Round($reply.eval_duration/1e9,2)
            output_tokens=$reply.eval_count;thinking_characters=$reply.message.thinking.Length
            answer_characters=$reply.message.content.Length;done_reason=$reply.done_reason;valid_summary=$valid
        }
    } catch {
        $result=[pscustomobject]@{mode=$testMode;seconds=[math]::Round($timer.Elapsed.TotalSeconds,2);error=$_.Exception.Message}
    }
    $results += $result
    $result | ConvertTo-Json -Compress | Write-Output
}
$outputDirectory = Join-Path (Split-Path -Parent $PSScriptRoot) 'validation'
$outputPath = Join-Path $outputDirectory 'summary-benchmark.json'
if ($Mode -ne 'both' -and (Test-Path -LiteralPath $outputPath)) {
    $previous = @(Get-Content -LiteralPath $outputPath -Raw | ConvertFrom-Json | Where-Object { $_.mode -ne $Mode })
    $results = $previous + $results
}
$results | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $outputPath -Encoding utf8
