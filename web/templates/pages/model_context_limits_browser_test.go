package pages

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestBrowserFunctional_ModelContextLimitsFollowSelection(t *testing.T) {
	raw, err := os.ReadFile("models.templ")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	extract := func(start, end string) string {
		a := strings.Index(source, start)
		if a < 0 {
			t.Fatal(start)
		}
		b := strings.Index(source[a:], end)
		if b < 0 {
			t.Fatal(end)
		}
		return source[a : a+b]
	}
	fixture := `<main id="reconnect-result"></main>
<select id="model_provider"><option>openai_compatible_vllm</option><option>openai</option></select>
<select id="model_id" onchange="handleModelChange()"><option value="large">large</option><option value="small">small</option><option value="unknown">unknown</option></select>
<input id="model_reasoning_effort"><input id="model_openai_compatible_custom_model">
<div id="model_fast_mode_field"></div><input id="model_fast_mode" type="checkbox"><input id="model_service_tier">
<input id="model_provider_context_window"><input id="model_provider_max_output_tokens">
<p id="model_provider_context_display"></p><p id="model_provider_max_output_display"></p>
<input id="model_context_window_cap"><input id="model_default_max_tokens"><input id="model_compaction_threshold">
<div id="model_output_limit_field"></div><span id="model_context_source"></span>
<script>
function isOpenAICompatibleProvider(p) { return p.indexOf('openai_compatible') === 0; }
function selectedModelOption() { return null; }
function setReasoningEffortOptions() {}
function updateTemperatureField() {}
` + extract("function handleModelChange()", "function modelSupportsTemperature") + extract("var openAICompatibleDiscoveredModels =", "function currentOpenAICompatibleDiscoveryIdentity") + `
try {
 var field = function(id) {return document.getElementById(id);};
 var expect = function(id, value) {if (field(id).value !== String(value)) throw Error(id + ': got '+field(id).value+', want '+value);};
 resetContextLimitFields();
 expect('model_context_window_cap',128000);
 populateContextLimitFields({context_window:262144});
 expect('model_context_window_cap',262144);
 openAICompatibleDiscoveredModels = {large:{context_length:262144},small:{context_length:8192,max_output_tokens:4096}};
 field('model_id').dispatchEvent(new Event('change'));
 expect('model_context_window_cap',262144); expect('model_provider_context_window',262144);
 field('model_context_window_cap').value='65536';
 setOpenAICompatibleModelValue('large','large',true);
 expect('model_context_window_cap',65536);
 field('model_id').value='small'; field('model_id').dispatchEvent(new Event('change'));
 expect('model_context_window_cap',8192); expect('model_default_max_tokens',2048);
 field('model_id').value='unknown'; field('model_id').dispatchEvent(new Event('change'));
 expect('model_provider_context_window',0); expect('model_provider_max_output_tokens',0); expect('model_context_window_cap',128000);
 field('model_openai_compatible_custom_model').value='large';syncOpenAICompatibleModel();
 expect('model_context_window_cap',262144);
 field('model_openai_compatible_custom_model').value='';syncOpenAICompatibleModel();
 expect('model_provider_context_window',0);expect('model_context_window_cap',128000);
 field('model_provider').value='openai';handleModelChange();
 if (!field('model_default_max_tokens').disabled || !field('model_output_limit_field').classList.contains('hidden')) throw Error('unsupported output setting still exposed');
 field('reconnect-result').setAttribute('data-test-result','pass');
} catch(e) {
 document.getElementById('reconnect-result').setAttribute('data-test-result','fail');
 document.getElementById('reconnect-result').setAttribute('data-test-error',String(e.stack));
}
</script>`
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_ModelContextDiscoveryAndEdit(t *testing.T) {
	var content bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	fixture := `<main id="reconnect-result"></main><script>
 window.htmx={process:function(){},ajax:function(){return Promise.resolve();}};
 window.fetch=function(){return Promise.resolve({ok:true,json:function(){return Promise.resolve({models:[{id:'large',context_length:262144},{id:'small',context_length:8192}]});}});};
 </script>` + content.String() + `<script>
 (async function(){
 var result=document.getElementById('reconnect-result');
 var field=function(id){return document.getElementById(id);};
 var wait=function(){return new Promise(function(resolve){setTimeout(resolve,20);});};
 try {
  openNewModelModal();
  var initial=selectedModelOption(field('model_provider').value,field('model_id').value);
  if(initial && initial.context_length && field('model_context_window_cap').value!==String(initial.context_length)) throw Error('catalog default window not filled');
  field('model_provider').value='openai_compatible_vllm'; toggleProviderFields();
  await wait();
  field('model_id').value='large';handleModelChange();
  if(field('model_context_window_cap').value!=='262144') throw Error('discovered window not filled');
  var values=new FormData(field('model_form'));
  if(values.get('context_window')!=='262144'||values.get('provider_context_window')!=='262144') throw Error('discovered values not submitted');
  field('new_model_modal').close();
  var edit=document.createElement('button');
  Object.assign(edit.dataset,{modelId:'test',modelName:'Saved Qwen',modelProvider:'openai_compatible',modelModel:'large',modelTemperature:'0',modelPresetSlug:'vllm',modelBaseUrl:'http://127.0.0.1:8000/v1/',modelContextWindowCap:'131072',modelProviderContextWindow:'262144'});
  populateModelEditForm(edit); await wait();
  if(field('model_context_window_cap').value!=='131072') throw Error('edit/discovery overwrote saved smaller window');
  field('model_id').value='small';handleModelChange();
  if(field('model_context_window_cap').value!=='8192'||field('model_default_max_tokens').value!=='2048') throw Error('switch failed to update budgets');
  field('model_base_url').value='http://127.0.0.1:9000/v1/';field('model_base_url').dispatchEvent(new Event('input'));
  if(field('model_provider_context_window').value!=='0'||field('model_context_window_cap').value!=='128000') throw Error('endpoint inherited limits');
  // Opening and saving an existing large output budget must not shrink it.
  Object.assign(edit.dataset,{modelContextWindowCap:'131072',modelProviderContextWindow:'131072',modelDefaultMaxTokens:'65536',modelProviderMaxOutputTokens:'65536'});
  populateModelEditForm(edit); await wait();
  if(field('model_default_max_tokens').value!=='65536') throw Error('valid compatible output budget reduced');
  if(new FormData(field('model_form')).get('default_max_tokens')!=='65536') throw Error('compatible output budget not submitted');
  applyDiscoveredModelLimits({context_length:65536,max_output_tokens:65536});
  if(field('model_default_max_tokens').value!=='16384') throw Error('reported maximum consumed the context window');
  field('model_default_max_tokens').value='65536';field('model_default_max_tokens').dispatchEvent(new Event('change'));
  if(field('model_default_max_tokens').value!=='16384') throw Error('oversized edited output not corrected');
  populateContextLimitFields({context_window:65536,provider_context_window:65536,provider_max_output_tokens:65536,default_max_tokens:65536});
  if(field('model_default_max_tokens').value!=='16384') throw Error('saved oversized output not corrected');
  field('model_provider').value='ollama';toggleProviderFields();
  if(field('model_default_max_tokens').disabled || field('model_output_limit_field').classList.contains('hidden')) throw Error('Ollama output control hidden');
  if(field('model_default_max_tokens').value!=='2048') throw Error('small Ollama default not bounded');
  populateContextLimitFields({context_window:131072,default_max_tokens:65536});
  if(field('model_default_max_tokens').value!=='65536') throw Error('saved Ollama output budget reduced');
  if(new FormData(field('model_form')).get('default_max_tokens')!=='65536') throw Error('Ollama output budget not submitted');
  field('model_provider').value='openai';toggleProviderFields();
  if(!field('model_default_max_tokens').disabled || !field('model_output_limit_field').classList.contains('hidden')) throw Error('unsupported output control exposed');
  Object.assign(edit.dataset,{modelProvider:'openai',modelModel:'gpt-6.1-sol',modelServiceTier:'priority',modelPresetSlug:''});
  populateModelEditForm(edit);
  if(field('model_fast_mode_field').classList.contains('hidden') || field('model_service_tier').disabled || !field('model_fast_mode').checked) throw Error('opening supported model did not initialize Fast mode');
  if(new FormData(field('model_form')).get('service_tier')!=='priority') throw Error('saved Fast default missing from form');
  field('model_provider').value='openai_compatible_vllm';toggleProviderFields();
  if(!field('model_fast_mode_field').classList.contains('hidden') || !field('model_service_tier').disabled || field('model_fast_mode').checked) throw Error('unsupported provider retained Fast mode control');
  if(new FormData(field('model_form')).has('service_tier')) throw Error('unsupported provider submits Fast mode');
  result.setAttribute('data-test-result','pass');
 }catch(e){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(e.stack));}
 })();</script>`
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_OllamaActiveContextDiscovery(t *testing.T) {
	var content bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	fixture := `<main id="reconnect-result"></main><script>
 window.htmx={process:function(){},ajax:function(){return Promise.resolve();}};
 var pendingContext=[];
 window.fetch=function(url,opts){
  if(String(url).indexOf('/models/ollama/context?')===0) return new Promise(function(resolve,reject){pendingContext.push({url:String(url),signal:opts.signal,resolve:function(windowSize){resolve({ok:true,json:function(){return Promise.resolve({context_length:windowSize,model_context_length:131072});}});},reject:reject});});
  return Promise.resolve({ok:true,json:function(){return Promise.resolve([{name:'loaded:latest'},{name:'unloaded:latest'}]);}});
 };
 </script>` + content.String() + `<script>
 (async function(){
 var result=document.getElementById('reconnect-result');
 var field=function(id){return document.getElementById(id);};
 var wait=function(){return new Promise(function(resolve){setTimeout(resolve,30);});};
 var expect=function(condition,message){if(!condition)throw Error(message);};
 var display=function(){return field('model_provider_context_display').textContent;};
 try {
  openNewModelModal();
  field('model_provider').value='ollama';toggleProviderFields();await wait();
  expect(pendingContext.length>0,'selection did not fetch ps');
  expect(display().includes('Not detected'),'invented detected context');
  field('model_context_window_cap').value='65536';field('model_default_max_tokens').value='10000';
  pendingContext.at(-1).resolve(32768);await wait();
  expect(display().includes('32,768'),'loaded window not displayed');
  expect(field('ollama_model_capacity_display').textContent.includes('131,072'),'model capacity not displayed separately');
  expect(field('model_context_window_cap').value==='65536','detection changed request budget');
  field('model_id').value='unloaded:latest';handleModelChange();
  expect(display().includes('Not detected'),'selection kept old detection');
  expect(field('ollama_model_capacity_display').textContent.includes('Not detected'),'selection kept old capacity');
  var stale=pendingContext.at(-1);
  field('model_id').value='loaded:latest';handleModelChange();
  expect(stale.signal.aborted,'old request not aborted');
  pendingContext.at(-1).resolve(16384);await wait();
  stale.resolve(99999);await wait();
  expect(display().includes('16,384'),'stale model response changed detection');
  field('model_id').value='unloaded:latest';handleModelChange();
  pendingContext.at(-1).resolve(0);await wait();expect(display().includes('Not detected'),'unloaded detection');
  field('model_id').value='loaded:latest';handleModelChange();
  pendingContext.at(-1).reject(Error('offline'));await wait();expect(display().includes('Not detected'),'failed discovery detection');
  handleModelChange();stale=pendingContext.at(-1);
  field('model_ollama_base_url').value='http://different:11434';ollamaEndpointChanged();
  stale.resolve(99999);await wait();expect(display().includes('Not detected'),'stale endpoint response changed detection');
  var edit=document.createElement('button');
  Object.assign(edit.dataset,{modelId:'saved',modelName:'Saved Ollama',modelProvider:'ollama',modelModel:'loaded:latest',modelTemperature:'0',modelOllamaBaseUrl:'http://saved:11434',modelContextWindowCap:'65536',modelDefaultMaxTokens:'10000',modelProviderContextWindow:'99999'});
  populateModelEditForm(edit);await wait();
  expect(field('model_id').value==='loaded:latest','edit replaced saved model');
  expect(pendingContext.at(-1).url.includes('loaded%3Alatest'),'edit did not discover saved model');
  expect(display().includes('Not detected'),'saved budget treated as detection');
  pendingContext.at(-1).resolve(8192);await wait();
  expect(display().includes('8,192'),'edit detection missing');
  expect(field('model_context_window_cap').value==='65536','edit budget reduced');
  expect(field('model_default_max_tokens').value==='10000','edit output reduced');
  var values=new FormData(field('model_form'));expect(values.get('context_window')==='65536'&&values.get('default_max_tokens')==='10000','saved budgets changed on submission');
  handleModelChange();stale=pendingContext.at(-1);closeModelModal();
  expect(stale.signal.aborted,'close did not abort');stale.resolve(99999);await wait();
  expect(display().includes('Not detected'),'closed modal accepted stale response');
  result.setAttribute('data-test-result','pass');
 }catch(error){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(error.stack));}
 })();
 </script>`
	runReconnectChromeFixture(t, fixture)
}
