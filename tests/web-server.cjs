// Isolated UI test server: no production database, credentials or real microphone.
// Run: node tests/web-server.cjs, then visit http://localhost:18080/.
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const web = path.resolve(__dirname, '../web');
const sampleID = '11111111-1111-4111-8111-111111111111';
const rec = {id:sampleID,filename:'Проверочная запись.wav',mime_type:'audio/wav',audio_size:1024,
  status:'failed',processing_stage:'summarizing',processing_error:'Проверочный сбой Ollama',
  created_at:new Date().toISOString(),updated_at:new Date().toISOString()};
let transcript = 'Сохранённая расшифровка остаётся доступной при ошибке создания резюме.';
let summary = '';
const speakerFixture = process.env.ECHO_SPEAKER_FIXTURE === '1';
const segments = speakerFixture ? [
  {start:0,end:2,text:'Давайте обсудим запуск приложения и задачи на следующую неделю.',speaker_id:'speaker_1'},
  {start:2,end:4,text:'Я проверю запись на телефоне и подготовлю список замечаний.',speaker_id:'speaker_2'},
  {start:4,end:6,text:'Хорошо. После проверки согласуем дату запуска.',speaker_id:'speaker_1'}
] : [];
if(speakerFixture) {rec.title='Обсуждение запуска приложения';rec.status='completed';rec.processing_stage='';summary='Собеседники договорились проверить приложение перед запуском.';transcript=segments.map(s=>s.text).join(' ');}
const types = {'.html':'text/html; charset=utf-8','.js':'text/javascript; charset=utf-8','.css':'text/css',
  '.svg':'image/svg+xml','.png':'image/png','.webmanifest':'application/manifest+json'};
const microphoneFixture = `<script>
  navigator.mediaDevices.getUserMedia = async () => {
    const context = new AudioContext();
    await context.resume();
    const oscillator = context.createOscillator();
    const destination = context.createMediaStreamDestination();
    oscillator.connect(destination); oscillator.start();
    for (const track of destination.stream.getTracks()) {
      const stop = track.stop.bind(track);
      track.stop = () => { stop(); context.close().catch(()=>{}); };
    }
    return destination.stream;
  };
</script>`;
const server = http.createServer((request,response)=>{
  const url = new URL(request.url,'http://localhost');
  response.setHeader('Cache-Control','no-store');
  response.setHeader('X-Echo-User','00000000-0000-4000-8000-000000009003');
  const json = value => {response.setHeader('Content-Type','application/json');response.end(JSON.stringify(value));};
  // This synthetic UI-only fixture does not test authentication. Use the Go
  // TestBrowserPreview fixture for real cookies, password hashing and login.
  if(url.pathname==='/api/auth/status') return json({authenticated:true,user_id:'00000000-0000-4000-8000-000000009003',username:'Тест',legacy_owner:false});
  if(url.pathname==='/api/stats') return json({total:1,completed:rec.status==='completed'?1:0,failed:rec.status==='failed'?1:0,processing:rec.status==='processing'?1:0,total_bytes:1024});
  if(url.pathname==='/api/recordings' && request.method==='GET') return json({items:[rec]});
  if(url.pathname==='/api/recordings' && request.method==='POST') {
    request.resume();rec.status='uploaded';rec.processing_stage='';rec.processing_error='';transcript='';summary='';rec.updated_at=new Date().toISOString();
    response.statusCode=201;return json(rec);
  }
  if(url.pathname===`/api/recordings/${sampleID}`) return json(rec);
  if(url.pathname===`/api/recordings/${sampleID}/result`) return json({recording:rec,transcript,summary,key_points:summary?['Проверочный тезис']:[],segments,diarization_status:speakerFixture?'completed':'unavailable'});
  if(url.pathname===`/api/recordings/${sampleID}/audio` && speakerFixture) {
    const data=Buffer.alloc(44+16000*2*8);data.write('RIFF');data.writeUInt32LE(data.length-8,4);data.write('WAVEfmt ',8);data.writeUInt32LE(16,16);data.writeUInt16LE(1,20);data.writeUInt16LE(1,22);data.writeUInt32LE(16000,24);data.writeUInt32LE(32000,28);data.writeUInt16LE(2,32);data.writeUInt16LE(16,34);data.write('data',36);data.writeUInt32LE(data.length-44,40);
    response.setHeader('Content-Type','audio/wav');response.setHeader('Accept-Ranges','bytes');
    const range=/^bytes=(\d+)-(\d*)$/.exec(request.headers.range||'');
    if(range){const start=Number(range[1]),end=range[2]?Math.min(Number(range[2]),data.length-1):data.length-1;response.statusCode=206;response.setHeader('Content-Range',`bytes ${start}-${end}/${data.length}`);response.setHeader('Content-Length',end-start+1);return response.end(data.subarray(start,end+1));}
    response.setHeader('Content-Length',data.length);return response.end(data);
  }
  if(url.pathname===`/api/recordings/${sampleID}/process`) {
    rec.status='processing';rec.processing_stage='queued';rec.processing_error='';rec.updated_at=new Date().toISOString();
    setTimeout(()=>{rec.status='completed';rec.processing_stage='';transcript=transcript||'Новая расшифровка';summary='Резюме успешно создано без повторного распознавания.';rec.updated_at=new Date().toISOString();},500);
    response.statusCode=202;return json({id:sampleID,status:'processing',queued:true});
  }
  if(url.pathname==='/storage-tests') {
    response.setHeader('Content-Type',types['.html']);return response.end(fs.readFileSync(path.join(__dirname,'storage-tests.html')));
  }
  const name = url.pathname==='/'?'index.html':url.pathname.slice(1);
  // Only direct public assets are served; .env and project sources are inaccessible.
  if(!name || name!==path.basename(name) || !fs.existsSync(path.join(web,name))) {response.statusCode=404;return response.end();}
  response.setHeader('Content-Type',types[path.extname(name)]||'application/octet-stream');
  let data=fs.readFileSync(path.join(web,name));
  if(name==='index.html') data=data.toString().replace('<script src="/drafts.js"',microphoneFixture+'<script src="/drafts.js"');
  response.end(data);
});
server.listen(18080,'127.0.0.1',()=>console.log('Isolated Echo UI test server: http://localhost:18080/ and /storage-tests'));
