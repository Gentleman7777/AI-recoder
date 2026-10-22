/* Durable, per-recording drafts. Chunks and metadata commit in one transaction. */
(() => {
  'use strict';
  function makeStore(name) {
  let database;
  function open() {
    if (!window.indexedDB) return Promise.reject(new Error('IndexedDB недоступна'));
    if (!database) database = new Promise((resolve, reject) => {
      const request = indexedDB.open(name, 1);
      request.onupgradeneeded = () => {
        request.result.createObjectStore('drafts', {keyPath:'id'});
        request.result.createObjectStore('chunks', {keyPath:['draftId','sequence']});
      };
      request.onerror = () => reject(request.error);
      request.onblocked = () => reject(new Error('Закройте другие вкладки Echo для обновления черновиков'));
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => { db.close(); database = null; };
        resolve(db);
      };
    }).catch(error => { database = null; throw error; });
    return database;
  }
  async function transaction(stores, mode, action) {
    const db = await open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(stores, mode);
      let result;
      tx.oncomplete = () => resolve(result);
      tx.onabort = () => reject(tx.error || new Error('Не удалось сохранить черновик'));
      tx.onerror = () => {}; // onabort reports the final transaction result.
      try { action(tx, value => { result = value; }); }
      catch (error) { tx.abort(); reject(error); }
    });
  }
  const chunkRange = id => IDBKeyRange.bound([id,0], [id,Number.MAX_SAFE_INTEGER]);
  const api = {
    // Browser-managed locks are released automatically when a tab crashes.
    async claim(id) {
      if (!navigator.locks) return () => {};
      let release;
      const held = new Promise(resolve => { release = resolve; });
      return new Promise((resolve,reject) => {
        navigator.locks.request(name === 'echo-recorder' ? `echo-draft-${id}` : `echo-draft-${name}-${id}`, {ifAvailable:true}, async lock => {
          if (!lock) { resolve(null); return; }
          resolve(release);
          await held;
        }).catch(reject);
      });
    },
    create(id, metadata) {
      return transaction(['drafts'], 'readwrite', tx => tx.objectStore('drafts').add({
        ...metadata, id, bytes:0, chunks:0, updatedAt:Date.now()
      }));
    },
    append(id, sequence, blob, metadata) {
      return transaction(['drafts','chunks'], 'readwrite', tx => {
        const store = tx.objectStore('drafts');
        const request = store.get(id);
        request.onsuccess = () => {
          if (!request.result) { tx.abort(); return; }
          tx.objectStore('chunks').add({draftId:id, sequence, blob});
          store.put({...request.result, ...metadata, bytes:request.result.bytes + blob.size,
            chunks:sequence+1, updatedAt:Date.now()});
        };
      });
    },
    update(id, metadata) {
      return transaction(['drafts'], 'readwrite', tx => {
        const store = tx.objectStore('drafts');
        const request = store.get(id);
        request.onsuccess = () => { if (request.result) store.put({...request.result,...metadata,updatedAt:Date.now()}); };
      });
    },
    list() {
      return transaction(['drafts'], 'readonly', (tx, done) => {
        const request = tx.objectStore('drafts').getAll();
        request.onsuccess = () => done(request.result.filter(draft => draft.bytes > 0 && !draft.uploaded)
          .sort((a,b) => b.updatedAt-a.updatedAt));
      });
    },
    load(id) {
      return transaction(['drafts','chunks'], 'readonly', (tx, done) => {
        const metadata = tx.objectStore('drafts').get(id);
        const chunks = tx.objectStore('chunks').getAll(chunkRange(id));
        chunks.onsuccess = () => {
          const rows = chunks.result;
          if (!metadata.result || rows.some((row,index) => row.sequence !== index)) {
            tx.abort(); return;
          }
          done({...metadata.result, blob:new Blob(rows.map(row => row.blob), {type:metadata.result.mimeType})});
        };
      });
    },
    remove(id) {
      return transaction(['drafts','chunks'], 'readwrite', tx => {
        tx.objectStore('drafts').delete(id);
        tx.objectStore('chunks').delete(chunkRange(id));
      });
    }
  };
  return {api, transaction};
  }
  const stores = new Map();
  const legacyStore = makeStore('echo-recorder').api;
  window.EchoDrafts = {
    forUser(id) {
      if(!/^[0-9a-f-]{36}$/i.test(id || '')) throw new Error('Не удалось открыть черновики аккаунта');
      if(stores.has(id))return stores.get(id);
      const store=makeStore(`echo-recorder-user-${id}`);
      store.api.importLegacy=async()=>{
        const migrate=async()=>{
          const legacy=legacyStore;
          for(const metadata of await legacy.list()) {
            const release=await legacy.claim(metadata.id);if(!release)continue;
            try {
              const draft=await legacy.load(metadata.id);
              await store.transaction(['drafts','chunks'],'readwrite',tx=>{
                const target=tx.objectStore('drafts');const existing=target.get(draft.id);
                existing.onsuccess=()=>{
                  if(existing.result)return;
                  const {blob,...fields}=draft;
                  target.add({...fields,chunks:1,bytes:blob.size});
                  tx.objectStore('chunks').add({draftId:draft.id,sequence:0,blob});
                };
              });
              await legacy.remove(draft.id);
            }finally{release();}
          }
        };
        if(navigator.locks)await navigator.locks.request('echo-legacy-drafts-migration',migrate);
        else await migrate();
      };
      stores.set(id,store.api);return store.api;
    }
  };
})();
