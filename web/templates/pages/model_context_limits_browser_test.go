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
<input id="model_provider_context_window"><input id="model_provider_max_output_tokens">
<input id="model_provider_context_display"><input id="model_provider_max_output_display">
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
  result.setAttribute('data-test-result','pass');
 }catch(e){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(e.stack));}
 })();</script>`
	runReconnectChromeFixture(t, fixture)
}
