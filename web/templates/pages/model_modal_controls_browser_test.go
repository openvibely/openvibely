package pages

import (
	"bytes"
	"context"
	"testing"
)

func TestBrowserFunctional_ModelModalDiscoveryAndDefaults(t *testing.T) {
	var content bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	fixture := `<main id="reconnect-result"></main><script>
 window.htmx={process:function(){},ajax:function(){return Promise.resolve();}};
 var returnedModels=[{id:'qwen',context_length:262144},{id:'other',context_length:8192}];
 var discoveryFails=false;
 var discoveryRequests=[];
 window.fetch=function(url,options){discoveryRequests.push({url:new URL(url,window.location.href),headers:options.headers});return discoveryFails ? Promise.reject(Error('Server unavailable')) : Promise.resolve({ok:true,json:function(){return Promise.resolve(url.indexOf('/models/ollama/available')===0 ? [{name:'installed:latest'}] : {models:returnedModels});}});};
 </script>` + content.String() + `<script>
 (async function(){
 var result=document.getElementById('reconnect-result');
 var field=function(id){return document.getElementById(id);};
 var wait=function(){return new Promise(function(resolve){setTimeout(resolve,30);});};
 var assert=function(ok,message){if(!ok)throw Error(message);};
 var hidden=function(id){return field(id).classList.contains('hidden');};
 var selectProvider=async function(provider){field('model_provider').value=provider;toggleProviderFields();await wait();};
 var change=function(id,value){field(id).value=value;field(id).dispatchEvent(new Event('change'));};
 var input=function(id,value){field(id).value=value;field(id).dispatchEvent(new Event('input'));};
 try {
  openNewModelModal();
  assert(field('model_api_key').placeholder.indexOf('sk-ant-')===0,'Anthropic key hint');
  assert(hidden('model_refresh') && hidden('model_base_url_field'),'Anthropic hides compatible connection controls');
  assert(field('model_compaction_threshold_mode').value==='default','default compaction mode');
  assert(hidden('model_worker_timeout_custom'),'default timeout hides numeric sentinel');
  assert(new FormData(field('model_form')).get('worker_timeout')==='0','default timeout wire value');
  for(var provider of ['openai_compatible_vllm','openai_compatible_lm_studio','openai_compatible_openrouter']) {
   await selectProvider(provider);
   assert(!hidden('model_refresh') && !hidden('model_base_url_field') && hidden('model_manual_id_field') && hidden('custom_provider_auth_fields') && hidden('model_oauth_setup'),'discovery controls '+provider);
   assert(field('model_id').options.length===2,'only discovered models '+provider);
   assert(!Array.from(field('model_id').options).some(function(o){return o.value==='local-model';}),'no fake model');
   assert(field('model_api_key').placeholder.indexOf('sk-ant-')===-1,'no Anthropic hint '+provider);
   assert(field('model_id').value==='qwen' && field('model_id').checkValidity(),'first model selected automatically');
  }
  await selectProvider('openai_compatible_vllm');
  change('model_id','other');field('model_refresh').click();await wait();
  assert(field('model_id').value==='other','refresh preserves a selection other than the first model');
  assert(field('model_api_key_label').textContent==='API key (optional)','local key optional');
  change('model_id','qwen');
  assert(field('model_compaction_threshold_mode').options[0].textContent.includes('235,929'),'automatic threshold');
  field('model_context_window_cap').value='131072';field('model_context_window_cap').dispatchEvent(new Event('change'));
  field('model_refresh').click();await wait();
  assert(field('model_id').value==='qwen' && field('model_context_window_cap').value==='131072','refresh preserves selected model and manual context');
  discoveryFails=true;field('model_refresh').click();await wait();
  assert(field('openai_compatible_discovery_status').textContent.includes('Server unavailable'),'discovery error');
  assert(hidden('model_manual_id_field'),'failure does not expose manual workaround');
  discoveryFails=false;returnedModels=[{id:'qwen',context_length:262144}];field('model_refresh').click();await wait();
  assert(field('model_id').options.length===1,'refresh removes stale models');
  assert(!field('openai_compatible_discovery_status').classList.contains('text-error'),'retry clears error');
  returnedModels=[{id:'replacement',context_length:8192}];field('model_refresh').click();await wait();
  assert(field('model_id').value==='replacement' && field('model_id').options.length===1,'missing selected model replaced');
  assert(field('model_context_window_cap').value==='8192','replacement uses its context limit');
  returnedModels=[];field('model_refresh').click();await wait();
  assert(!field('model_id').value && !field('model_id').checkValidity() && !field('model_openai_compatible_custom_model').value,'empty refresh clears selected model and blocks save');
  returnedModels=[{id:'qwen',context_length:262144}];field('model_refresh').click();await wait();

  change('model_worker_timeout_mode','custom');input('model_worker_timeout_custom','120');
  change('model_max_workers_mode','custom');input('model_max_workers_custom','3');
  change('model_compaction_threshold_mode','custom');input('model_compaction_threshold_custom','100000');
  var values=new FormData(field('model_form'));
  assert(values.get('worker_timeout')==='120' && values.get('model_max_workers')==='3' && values.get('compaction_threshold')==='100000','custom overrides submitted');
  await selectProvider('openai');
  assert(hidden('model_refresh') && hidden('openai_compatible_fields') && hidden('model_base_url_field'),'OpenAI controls restored');
  assert(field('model_worker_timeout_custom').value==='120','provider change preserves timeout');
  assert(field('model_api_key').placeholder==='API key','OpenAI key hint');
  field('model_openai_auth_type').value='oauth';toggleOpenAIAuthFields();
  assert(hidden('api_key_field'),'OpenAI OAuth hides key');
  await selectProvider('anthropic');
  assert(field('model_api_key').placeholder.indexOf('sk-ant-')===0,'Anthropic hint restored');
  field('model_anthropic_auth_type').value='oauth';toggleAnthropicAuthFields();
  assert(hidden('api_key_field'),'Anthropic OAuth hides key');
  await selectProvider('ollama');
  assert(!hidden('model_refresh') && hidden('api_key_field') && !field('model_ollama_custom_model'),'Ollama uses one discovered dropdown');
  assert(field('model_id').value==='installed:latest','Ollama selects installed model');
  assert(!hidden('model_ollama_base_url_field') && !!(field('model_ollama_base_url').compareDocumentPosition(field('model_id')) & Node.DOCUMENT_POSITION_FOLLOWING),'Ollama URL precedes model selection');
  assert(new FormData(field('model_form')).get('ollama_base_url')==='http://localhost:11434','Ollama URL still submitted');
  await selectProvider('mixture');
  assert(hidden('model_field') && !field('model_id').required,'mixture not blocked by hidden select');
  assert(hidden('model_ollama_base_url_field'),'Ollama URL hidden after provider switch');
  await selectProvider('openai_compatible_custom');
  assert(!hidden('model_manual_id_field') && hidden('model_field'),'custom manual-only entry');
  input('model_openai_compatible_custom_model','manual-id');
  assert(new FormData(field('model_form')).get('model')==='manual-id','manual custom model submitted');
  input('model_custom_models_url','http://localhost:8000/v1/models');
  assert(hidden('model_manual_id_field') && !hidden('model_refresh'),'custom models URL enables discovery');
  cancelOpenAICompatibleDiscovery();
  field('model_id').value='';discoverOpenAICompatibleModels();await wait();
  assert(field('model_id').value==='qwen','single discovered model selected');
  input('model_custom_models_url','');cancelOpenAICompatibleDiscovery();
  assert(!hidden('model_manual_id_field') && hidden('model_refresh'),'removing models URL restores manual');
  change('model_worker_timeout_mode','default');
  assert(new FormData(field('model_form')).get('worker_timeout')==='0' && hidden('model_worker_timeout_custom'),'restore default');
  field('new_model_modal').close();
  var edit=document.createElement('button');
  Object.assign(edit.dataset,{modelId:'saved',modelName:'Saved',modelProvider:'openai_compatible',modelModel:'qwen',modelPresetSlug:'vllm',modelBaseUrl:'http://127.0.0.1:8000/v1/',modelContextWindowCap:'131072',modelWorkerTimeout:'240',modelMaxWorkers:'4',modelCompactionThreshold:'80000'});
  populateModelEditForm(edit);await wait();
  assert(field('model_worker_timeout_mode').value==='custom' && field('model_worker_timeout_custom').value==='240','saved timeout restored');
  assert(field('model_max_workers_custom').value==='4' && field('model_compaction_threshold_custom').value==='80000','saved limits restored');
  assert(new FormData(field('model_form')).get('context_window')==='131072','saved context preserved on discovery');
  field('new_model_modal').close();openNewModelModal();
  assert(field('model_worker_timeout_mode').value==='default' && field('model_max_workers_mode').value==='default','new form resets saved overrides');
  returnedModels=[{id:'kimi-k3',context_length:262144}];
  await selectProvider('openai_compatible_moonshot');
  assert(field('model_id').value==='kimi-k3' && !hidden('reasoning_effort_field'),'automatic selection updates reasoning controls');
  // Reproduce editing a saved NIM configuration and switching it to local vLLM.
  field('new_model_modal').close();
  returnedModels=[{id:'nvidia/model',context_length:131072}];
  Object.assign(edit.dataset,{modelId:'saved-nim',modelProvider:'openai_compatible',modelModel:'nvidia/model',modelPresetSlug:'nvidia_nim',modelBaseUrl:'https://integrate.api.nvidia.com/v1/',modelModelsUrl:'https://integrate.api.nvidia.com/v1/models',modelApiKey:'saved-nim-secret',modelAuthMethod:'api_key',modelExtraHeadersJson:'{"X-Saved-Secret":"old-secret"}'});
  populateModelEditForm(edit);await wait();
  assert(discoveryRequests.at(-1).url.searchParams.get('config_id')==='saved-nim','unchanged edit uses saved connection');
  returnedModels=[{id:'qwen',context_length:262144}];
  var beforeSwitch=discoveryRequests.length;
  await selectProvider('openai_compatible_vllm');
  assert(discoveryRequests.length>beforeSwitch && field('model_id').value==='qwen','saved NIM to vLLM auto-discovery');
  var request=discoveryRequests.at(-1);
  assert(request.url.searchParams.get('base_url')==='http://127.0.0.1:8000/v1/','new endpoint');
  assert(!request.url.searchParams.has('config_id') && !request.url.searchParams.has('models_url'),'no old connection or models endpoint');
  assert(!request.headers['X-OpenAI-Compatible-API-Key'] && !request.headers['X-OpenAI-Compatible-Extra-Headers'],'no saved secrets forwarded');
  assert(field('model_config_id').value==='saved-nim','edit still updates same saved record');
  field('model_refresh').click();await wait();
  assert(discoveryRequests.length>beforeSwitch+1 && field('model_id').value==='qwen','refresh after provider switch');
  assert(field('model_refresh').querySelector('svg') && !field('model_refresh').textContent.trim() && field('model_refresh').getAttribute('aria-label')==='Refresh models','accessible icon-only refresh');
  // Explicitly entered credentials may be used for the new server.
  input('model_api_key','new-server-key');cancelOpenAICompatibleDiscovery();field('model_refresh').click();await wait();
  assert(discoveryRequests.at(-1).headers['X-OpenAI-Compatible-API-Key']==='new-server-key','new credential used');
  // Returning to the saved provider reuses its saved endpoint only when they match.
  field('model_api_key').value='saved-nim-secret';
  await selectProvider('openai_compatible_nvidia_nim');
  field('model_custom_models_url').value='https://integrate.api.nvidia.com/v1/models';
  field('model_refresh').click();await wait();
  assert(discoveryRequests.at(-1).url.searchParams.get('config_id')==='saved-nim','saved connection restored');
  result.setAttribute('data-test-result','pass');
 }catch(e){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(e.stack));}
 })();
 </script>`
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_CustomOAuthBeforeCreate(t *testing.T) {
	t.Setenv("OAUTH_REDIRECT_MODE", "localhost_manual")
	var content bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	fixture := `<main id="reconnect-result"></main><script>
 window.htmx={process:function(){}};
 var requests=[];
 var connected=false;
 window.open=function(){return {location:{href:''},close:function(){}};};
 window.fetch=function(url,options){
  options=options||{};requests.push({url:url,options:options});
  var data={};
  if(url.indexOf('/models/oauth/manual-complete')===0){connected=true;data={};}
  else if(url.indexOf('/models/oauth/setup/status')===0)data={connected:connected};
  else if(url.indexOf('/models/oauth/setup')===0)data={session_id:'temporary-sign-in',authorization_url:'https://auth.example/authorize',opened:false};
  else if(url.indexOf('/models/openai-compatible/available')===0){
   if(options.headers['X-Model-OAuth-Setup']!=='temporary-sign-in')return Promise.reject(Error('Discovery attempted without temporary authorization'));
   data={models:[{id:'authorized-model',context_length:262144}]};
  }
  return Promise.resolve({ok:true,json:function(){return Promise.resolve(data);}});
 };
 </script>` + content.String() + `<script>
 (async function(){var f=function(id){return document.getElementById(id);};
 try{
 openNewModelModal();f('model_provider').value='openai_compatible_custom';toggleProviderFields();
 f('model_name').value='Custom model';f('model_base_url').value='https://api.example/v1';
 f('model_custom_auth_method').value='oauth';toggleCustomProviderAuthFields();
 f('model_custom_models_url').value='https://api.example/models';scheduleAutoDiscoverOpenAICompatibleModels();cancelOpenAICompatibleDiscovery();
 discoverOpenAICompatibleModels();
 if(requests.length)throw Error('Discovery ran before sign-in');
 if(f('model_submit_btn').textContent!=='Create'||f('model_id').checkValidity())throw Error('Create incorrectly allows missing model');
 if(f('model_oauth_setup').classList.contains('hidden'))throw Error('Cannot sign in before creating');
 var before=function(a,b){return !!(f(a).compareDocumentPosition(f(b)) & Node.DOCUMENT_POSITION_FOLLOWING);};
 if(!before('model_base_url','model_custom_auth_method') || !before('model_custom_auth_method','model_api_key') || !before('model_custom_models_url','model_id') || !before('model_custom_token_url','model_oauth_setup') || !before('model_oauth_setup','model_id'))throw Error('Connection settings must precede sign-in and model selection');
 await startCustomModelOAuth(f('model_oauth_setup').querySelector('button'));
 await new Promise(function(resolve){setTimeout(resolve,50);});
 if(f('model_id').value)throw Error('Model selected before manual completion');
 var callback=f('model_oauth_setup_callback_url');
 if(!callback || !f('new_model_modal').contains(callback))throw Error('Manual callback outside modal');
 callback.closest('details').open=true;
 callback.value='http://localhost:1455/callback?code=test-code&state=test-state';
 callback.parentElement.querySelector('button').click();
 await new Promise(function(resolve){setTimeout(resolve,50);});
 var completion=requests.find(function(r){return r.url.indexOf('/models/oauth/manual-complete')===0;});
 if(!completion || JSON.parse(completion.options.body).callback_url.indexOf('code=test-code')===-1)throw Error('Manual callback not submitted');
 if(!f('new_model_modal').open || callback.value)throw Error('Manual completion closed modal or kept callback');
 if(f('model_id').value!=='authorized-model'||f('model_config_id').value)throw Error('Expected selected model without a saved configuration');
 if(f('model_submit_btn').textContent!=='Create'||!f('model_id').checkValidity())throw Error('Cannot create completed model');
 if(requests.some(function(r){return r.url==='/models';}))throw Error('Model saved before Create');
 var data=new FormData(f('model_form'));
 if(data.get('model')!=='authorized-model'||data.get('oauth_setup_session')!=='temporary-sign-in')throw Error('Create missing selected model or sign-in session');
 f('model_custom_auth_method').value='api_key';
 f('model_custom_auth_method').dispatchEvent(new Event('change'));
 cancelOpenAICompatibleDiscovery();
 if(new FormData(f('model_form')).get('oauth_setup_session'))throw Error('API key form retains OAuth session');
 if(!requests.some(function(r){return r.options.method==='DELETE' && r.options.headers['X-Model-OAuth-Setup']==='temporary-sign-in';}))throw Error('Auth switch did not cancel OAuth');
 if(!f('model_oauth_setup').classList.contains('hidden'))throw Error('OAuth setup still visible for API key');
 f('model_provider').value='openai';toggleProviderFields();
 if(!f('custom_provider_auth_fields').classList.contains('hidden'))throw Error('Custom settings leaked after provider switch');
 closeModelModal();
 if(f('model_oauth_setup_session').value)throw Error('Cancelled form retains sign-in');
 if(!requests.some(function(r){return r.options.method==='DELETE';}))throw Error('Temporary session not discarded');
 openNewModelModal();
 if(!f('custom_provider_auth_fields').classList.contains('hidden'))throw Error('Custom settings leaked into Anthropic');
 if(f('model_oauth_setup_session').value || !f('model_oauth_setup').classList.contains('hidden'))throw Error('Sign-in leaked into another provider');
 f('reconnect-result').setAttribute('data-test-result','pass');
 }catch(e){f('reconnect-result').setAttribute('data-test-result','fail');f('reconnect-result').setAttribute('data-test-error',String(e.stack));}
 })();</script>`
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_OllamaDiscovery(t *testing.T) {
	var content bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	fixture := `<main id="reconnect-result"></main><script>
 window.htmx={process:function(){}};
 var models=[{name:'installed:a'},{model:'installed:b'}], fail=false, pending=null, requested=[];
 window.fetch=function(url,options){
  requested.push(url);
  if(pending)return new Promise(function(resolve){pending.resolve=resolve;});
  return Promise.resolve({ok:!fail,json:function(){return Promise.resolve(fail?{error:'Server offline'}:models);}});
 };
 </script>` + content.String() + `<script>
 (async function(){
 var f=function(id){return document.getElementById(id);};
 var wait=function(){return new Promise(function(resolve){setTimeout(resolve,40);});};
 var assert=function(ok,message){if(!ok)throw Error(message);};
 try {
 openNewModelModal();f('model_provider').value='ollama';toggleProviderFields();await wait();
 assert(f('model_id').value==='installed:a' && f('model_id').options.length===2,'installed models only, first selected');
 assert(new FormData(f('model_form')).get('model')==='installed:a','selected model submitted');
 f('model_id').value='installed:b';f('model_refresh').click();await wait();
 assert(f('model_id').value==='installed:b','refresh preserves selection');
 models=[{name:'replacement:model'}];f('model_refresh').click();await wait();
 assert(f('model_id').value==='replacement:model' && f('model_id').options.length===1,'deleted selection removed on refresh');
 models=[];f('model_refresh').click();await wait();
 assert(!f('model_id').value && !f('model_id').checkValidity(),'empty refresh removes deleted selection');

 f('model_ollama_base_url').value='http://localhost:11435';f('model_ollama_base_url').dispatchEvent(new Event('input'));
 assert(!f('model_id').value && !f('model_id').checkValidity(),'changing server clears previous model');
 models=[];runAutoDiscoverOpenAICompatibleModels();await wait();
 assert(requested[requested.length-1].includes('11435'),'discovery uses changed server');
 assert(!f('model_id').checkValidity() && f('openai_compatible_discovery_status').textContent.includes('No models installed'),'empty server blocks create');
 fail=true;f('model_refresh').click();await wait();
 assert(f('openai_compatible_discovery_status').textContent.includes('Server offline') && !f('model_id').checkValidity(),'failure shown without fallback models');
 fail=false;models=[{name:'new:model'}];f('model_refresh').click();await wait();
 assert(f('model_id').value==='new:model' && !f('openai_compatible_discovery_status').classList.contains('text-error'),'refresh recovers');
 pending={};f('model_refresh').click();
 f('model_provider').value='anthropic';toggleProviderFields();var original=f('model_id').value;
 pending.resolve({ok:true,json:function(){return Promise.resolve([{name:'stale:model'}]);}});pending=null;await wait();
 assert(f('model_id').value===original,'late discovery cannot overwrite another provider');
 var edit=document.createElement('button');
 Object.assign(edit.dataset,{modelId:'saved',modelName:'Saved',modelProvider:'ollama',modelModel:'saved:model',modelOllamaBaseUrl:'http://localhost:11436'});
 models=[{name:'saved:model'},{name:'other:model'}];populateModelEditForm(edit);await wait();
 assert(f('model_id').value==='saved:model','editing preserves saved model');
 assert(requested[requested.length-1].includes('11436'),'editing discovers saved server');
 models=[];f('model_refresh').click();await wait();
 assert(!f('model_id').value && !f('model_id').checkValidity(),'uninstalled saved model cannot be resubmitted');
 f('reconnect-result').setAttribute('data-test-result','pass');
 } catch(error){f('reconnect-result').setAttribute('data-test-result','fail');f('reconnect-result').setAttribute('data-test-error',String(error.stack));}
 })();</script>`
	runReconnectChromeFixture(t, fixture)
}
