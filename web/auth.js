(() => {
 'use strict';
 const $=selector=>document.querySelector(selector);
 let user=null, mode='login', pending=false;
 const channel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('echo-auth') : null;
 const showError=message=>{$('#auth-error').textContent=message;$('#auth-error').hidden=false;};
 function setMode(next) {
  mode=next;
  const registration=mode==='register';
  $('#auth-form').reset();$('#auth-error').hidden=true;
  $('#auth-title').innerHTML=registration?'Ваше место<br>для важных мыслей.':'Ваши мысли.<br>Только для вас.';
  $('#auth-description').textContent=registration?'Создайте аккаунт. Ваши записи будут доступны только вам.':'Войдите, чтобы открыть свои записи.';
  $('#auth-submit').textContent=registration?'Создать аккаунт':'Войти';
  $('#auth-password').autocomplete=registration?'new-password':'current-password';
  $('#auth-password').minLength=registration?12:1;$('#auth-password').type='password';
  $('#auth-confirm').hidden=!registration;$('#auth-confirm-label').hidden=!registration;$('#auth-confirm').required=registration;
  $('#auth-hint').hidden=!registration;
  $('#password-toggle').textContent='Показать';$('#password-toggle').setAttribute('aria-label','Показать пароль');$('#password-toggle').setAttribute('aria-pressed','false');
  $('#auth-login-tab').setAttribute('aria-pressed',String(!registration));$('#auth-register-tab').setAttribute('aria-pressed',String(registration));
  document.title=registration?'Регистрация — Echo':'Вход — Echo';
 }
 function lock() {
  const wasAuthenticated=!!user;user=null;
  document.body.classList.remove('signed-in');$('#auth-screen').hidden=false;
  $('#auth-password').value='';$('#auth-confirm').value='';
  if(wasAuthenticated){
   setMode('login');$('#auth-form').hidden=false;$('#auth-tabs').hidden=false;
   $('#current-user').textContent='Личное пространство';$('#current-avatar').textContent='E';
   window.dispatchEvent(new Event('echo:locked'));$('#auth-username').focus();
  }
 }
 function unlock(data) {
  if(!data.user_id)throw new Error('Missing account');
  user={id:data.user_id,name:data.username,legacyOwner:!!data.legacy_owner};
  $('#auth-form').reset();$('#auth-screen').hidden=true;document.body.classList.add('signed-in');
  $('#current-user').textContent=user.name;$('#current-avatar').textContent=Array.from(user.name)[0]?.toUpperCase()||'E';
  document.querySelectorAll('.logout-button').forEach(node=>node.title=`Выйти: ${user.name}`);
  window.dispatchEvent(new Event('echo:login'));
  channel?.postMessage('changed');
 }
 async function status() {
  $('#auth-retry').hidden=true;
  try {
   const response=await fetch('/api/auth/status',{cache:'no-store'});if(!response.ok)throw new Error();
   const data=await response.json();
   if(data.authenticated){unlock(data);return;}
   lock();setMode('login');$('#auth-form').hidden=false;$('#auth-tabs').hidden=false;$('#auth-username').focus();
  }catch{
   lock();$('#auth-form').hidden=true;$('#auth-tabs').hidden=true;
   $('#auth-description').textContent='Не удалось открыть приложение. Проверьте соединение и попробуйте ещё раз.';$('#auth-retry').hidden=false;
  }
 }
 $('#auth-login-tab').addEventListener('click',()=>{if(!pending)setMode('login');});
 $('#auth-register-tab').addEventListener('click',()=>{if(!pending)setMode('register');});
 $('#auth-form').addEventListener('submit',async event=>{
  event.preventDefault();if(pending)return;$('#auth-error').hidden=true;
  const registration=mode==='register',password=$('#auth-password').value;
  if(registration&&password!==$('#auth-confirm').value){showError('Пароли не совпадают. Проверьте ещё раз.');return;}
  if(new TextEncoder().encode(password).length>72){showError('Этот пароль слишком длинный. Выберите немного короче.');return;}
  pending=true;$('#auth-submit').disabled=true;$('#auth-submit').textContent='Открываем…';
  try{
   const response=await fetch(`/api/auth/${registration?'register':'login'}`,{method:'POST',cache:'no-store',headers:{'Content-Type':'application/json','X-Echo-Request':'1'},body:JSON.stringify({username:$('#auth-username').value.trim(),password})});
   const data=await response.json();if(!response.ok){showError(data.error||'Не удалось войти. Попробуйте ещё раз.');return;}
   unlock(data);
  }catch{showError('Не удалось подключиться. Попробуйте ещё раз.');}
  finally{pending=false;$('#auth-submit').disabled=false;$('#auth-submit').textContent=registration?'Создать аккаунт':'Войти';}
 });
 $('#password-toggle').addEventListener('click',()=>{
  const show=$('#auth-password').type==='password';$('#auth-password').type=show?'text':'password';
  $('#password-toggle').textContent=show?'Скрыть':'Показать';$('#password-toggle').setAttribute('aria-label',show?'Скрыть пароль':'Показать пароль');$('#password-toggle').setAttribute('aria-pressed',String(show));
 });
 $('#auth-retry').addEventListener('click',status);
 window.EchoAuth={get authenticated(){return !!user;},get user(){return user;},lock,
  async verify(){
   const previous=user?.id;if(!previous)return;
   try{const response=await fetch('/api/auth/status',{cache:'no-store'});if(!response.ok)return;const data=await response.json();
    if(user?.id===previous&&(!data.authenticated||data.user_id!==previous))lock();
   }catch{/* Offline is handled by regular requests. */}
  },
  async logout(){
   const response=await fetch('/api/auth/logout',{method:'POST',headers:{'X-Echo-Request':'1'}});
   if(!response.ok)throw new Error('Не удалось выйти. Попробуйте ещё раз.');lock();channel?.postMessage('changed');
  }
 };
 if(channel)channel.onmessage=()=>window.EchoAuth.verify();
 window.addEventListener('focus',()=>window.EchoAuth.verify());
 status();
})();
