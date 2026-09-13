// Actual native host + extension controller. Browser APIs/credentials are synthetic.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp, mkdir, writeFile, rm, copyFile, appendFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import net from 'node:net';
import {once} from 'node:events';
import {fileURLToPath} from 'node:url';
import {createExtensionController} from '../chrome-extension/core.js';

const project = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const binary = process.argv[2];
let pluginDir = path.resolve(process.argv[3] || path.join(project, 'web-accounts'));
const previousLibrary=process.argv[4] ? path.resolve(process.argv[4]) : '';
const expectedVersion = '0.1.2';
const newLibrary=path.join(pluginDir, process.platform==='darwin'?'web-accounts.dylib':(process.platform==='win32'?'web-accounts.dll':'web-accounts.so'));
assert(binary, 'usage: node integration/extension-host-smoke.mjs BACKEND_BINARY [PLUGIN_DIR]');
const root = await mkdtemp(path.join(tmpdir(), 'cpamc-extension-test-'));
const socket = net.createServer();
socket.listen(0, '127.0.0.1'); await once(socket, 'listening');
const port = socket.address().port; await new Promise(resolve => socket.close(resolve));
const baseURL = `http://127.0.0.1:${port}`;
const key = 'extension-management-fixture-not-real';
const apiKey = 'extension-api-fixture-not-real';
let processHandle;
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
function area() {
  const values = {};
  return {
    values,
    async get(keys) { if (!keys) return {...values}; const out={}; for (const k of typeof keys==='string'?[keys]:keys) if (k in values) out[k]=values[k]; return out; },
    async set(v) { Object.assign(values, v); },
    async remove(keys) { for (const k of typeof keys==='string'?[keys]:keys) delete values[k]; },
    async clear() { for (const k of Object.keys(values)) delete values[k]; },
    async setAccessLevel({accessLevel}) { assert.equal(accessLevel, 'TRUSTED_CONTEXTS'); }
  };
}
const permissionRequests=[], captured=[];
const chromeAPI={
  storage:{local:area(),session:area()},
  permissions:{async request(v){permissionRequests.push(v);return true;},async contains(){return true;}},
  cookies:{async get(v){captured.push(v);assert.deepEqual(v,{url:'https://claude.ai/',name:'sessionKey'});return {value:'extension-claude-fixture-not-real',domain:'.claude.ai'};}},
  tabs:{async query(v){assert.deepEqual(v,{url:['https://www.kimi.ai/*']});return [{id:7,url:'https://www.kimi.ai/'}];},async create(){assert.fail('fixtures should already have a session');}},
  scripting:{async executeScript(v){assert.equal(v.target.tabId,7);assert.equal(v.world,'ISOLATED');assert.deepEqual(v.args,['https://www.kimi.ai']);return [{result:{token:'extension-kimi-fixture-not-real'}}];}}
};
try {
  await mkdir(path.join(root,'auth'));
  if(previousLibrary) {
    pluginDir=path.join(root,'plugins'); await mkdir(pluginDir);
    await copyFile(previousLibrary,path.join(pluginDir,`web-accounts-v0.1.0${path.extname(newLibrary)}`));
  }
  const config=path.join(root,'config.yaml');
  await writeFile(config,`host: 127.0.0.1\nport: ${port}\nauth-dir: ${JSON.stringify(path.join(root,'auth'))}\napi-keys: [${apiKey}]\nremote-management:\n  allow-remote: false\n  secret-key: ${key}\n  disable-control-panel: true\nplugins:\n  enabled: true\n  dir: ${JSON.stringify(pluginDir)}\n  configs:\n    web-accounts:\n      enabled: true\nlogging-to-file: false\n`,{mode:0o600});
  processHandle=spawn(path.resolve(binary),['-local-model','-config',config],{cwd:root,env:{PATH:process.env.PATH,HOME:root},stdio:'ignore'});
  let ready=false;
  for(let i=0;i<150;i++) {
    assert.equal(processHandle.exitCode,null,'isolated host exited');
    try {const r=await fetch(baseURL+'/v0/management/plugins',{headers:{Authorization:`Bearer ${key}`}});if(r.ok){ready=true;break;}} catch {}
    await delay(100);
  }
  assert(ready,'isolated host readiness timeout');
  if(previousLibrary) {
    const nextPath=path.join(pluginDir,`web-accounts-v${expectedVersion}${path.extname(newLibrary)}`);
    await copyFile(newLibrary,nextPath);
    const patched=await fetch(baseURL+'/v0/management/plugins/web-accounts/enabled',{method:'PATCH',headers:{Authorization:`Bearer ${key}`,'Content-Type':'application/json'},body:JSON.stringify({enabled:true})});
    assert(patched.ok,'same-state enabled patch must be accepted');
    // The real host skips identical config bytes. A comment changes the hash
    // without changing routing, settings, credentials or plugin enablement.
    await appendFile(config,`\n# Reload native web-accounts v${expectedVersion}\n`);
    let replaced=false;
    for(let i=0;i<100;i++) {
      const state=await(await fetch(baseURL+'/v0/management/plugins',{headers:{Authorization:`Bearer ${key}`}})).json();
      replaced=state.plugins?.some(p=>p.id==='web-accounts' && p.path===nextPath && p.metadata?.version===expectedVersion && p.registered===true && p.effective_enabled===true);
      if(replaced)break; await delay(100);
    }
    assert(replaced,'native hot replacement must load the new pathname without process restart');
    console.log('PASS: native versioned-path hot replacement without backend restart.');
  }
  const state = await (await fetch(baseURL+'/v0/management/plugins',{headers:{Authorization:`Bearer ${key}`}})).json();
  const native = state.plugins.find(p => p.id === 'web-accounts');
  assert.equal(native.metadata.version, expectedVersion);
  const menu = native.menus.find(m => m.menu === 'Cuentas web');
  assert(menu, 'native sidebar menu must be registered');
  assert.equal(menu.path, '/v0/resource/plugins/web-accounts/accounts');
  const page = await fetch(baseURL+menu.path);
  assert.equal(page.status, 200, 'static resource must not require Management credentials');
  assert.match(page.headers.get('content-type'), /text\/html/);
  assert.match(page.headers.get('content-security-policy'), /default-src 'none'/);
  assert.match(await page.text(), /Usar sesión de CPAMC/);
  assert.equal((await fetch(baseURL+menu.path+'/unknown')).status, 404);
  console.log('PASS: native Cuentas web sidebar menu and public static resource on actual host.');
  const controller=createExtensionController({chromeAPI,fetchImpl:fetch});
  await controller.pairBackend(baseURL,key);
  const before=await (await fetch(baseURL+'/v0/management/auth-files',{headers:{Authorization:`Bearer ${key}`}})).json();
  const baselineNames=new Set(before.files.map(file=>file.name));
  const added=[];
  for(const provider of ['claude','kimi','qoder']) {
    const result=await controller.connectProvider(provider,{expectedBaseURL:baseURL,personalAccessToken:provider==='qoder'?'extension-qoder-fixture-not-real':undefined});
    assert.match(result.name,new RegExp(`^web-accounts-${provider}-[a-f0-9-]+\\.json$`));
    added.push(result.name);
  }
  assert.equal(new Set(added).size,3,'generated names must not overwrite each other');
  // Allow the host's one-second watcher debounce to settle before checking IDs.
  await delay(1500);
  const listing=await (await fetch(baseURL+'/v0/management/auth-files',{headers:{Authorization:`Bearer ${key}`}})).json();
  const imported=listing.files.filter(file=>!baselineNames.has(file.name));
  assert.deepEqual(imported.map(file=>file.name).sort(), [...added].sort(), 'all three uploaded files must be imported without unrelated additions');
  assert(!JSON.stringify(listing).includes('fixture-not-real'),'listing must not expose credentials');
  let models;
  for(let i=0;i<50;i++) {models=await(await fetch(baseURL+'/v1/models',{headers:{Authorization:`Bearer ${apiKey}`}})).json();if(models.data?.some(x=>x.id.startsWith('web-qoder/')))break;await delay(100);}
  for(const prefix of ['web-claude/','web-kimi/','web-qoder/']) assert(models.data.some(x=>x.id.startsWith(prefix)),`missing account models ${prefix}`);
  assert(!JSON.stringify(chromeAPI.storage.local.values).includes('fixture'),'no secrets in persistent extension storage');
  await controller.disconnect();
  assert(!JSON.stringify(chromeAPI.storage.session.values).includes(key),'disconnect must delete management key');
  assert.deepEqual(captured,[{url:'https://claude.ai/',name:'sessionKey'}],'only targeted cookie read');
  console.log('PASS: extension pairing, Claude/Kimi/Qoder uploads, native model registration and disconnect against actual isolated backend.');
  console.log('Browser APIs and account credentials are synthetic; no live account inference or active-service changes.');
} finally {
  if(processHandle && processHandle.exitCode===null){const ended=once(processHandle,'exit');processHandle.kill('SIGTERM');await Promise.race([ended,delay(5000)]);if(processHandle.exitCode===null){processHandle.kill('SIGKILL');await ended;}}
  await rm(root,{recursive:true,force:true});
}
