package components

import "testing"

func TestBrowserFunctional_ChatToolBackticksStayWithinBoundaries(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const response = stream, tick = String.fromCharCode(96);
  // A truncated command preview leaves an unmatched backtick. Previously it
  // paired with the next prose delimiter and shifted every following pair.
  for (const output of ['plain output', 'output '+tick+' with an unmatched fence'+String.fromCharCode(10)+'~~~']) {
  const text = '[Using tool: bash | $ echo '+tick+'truncated…]\n[Tool bash done]\n' + output + '\n[/Tool]\n' +
   'The saved-workbook tests exposed a second issue: '+tick+'!'+tick+' inside a table header was treated as a worksheet separator.\n\n' +
   '[Using tool: bash | $ cat >> docs/TRACE.md <<\'EOF\' ### Structured selectors '+tick+'TestSharedFormulaStructuredHeadersStayLiteral'+tick+' explicit '+tick+'#Data'+tick+'…]\n' +
   '[Tool bash done]\n5 files changed\n[/Tool]\nFixed headers containing '+tick+'!'+tick+'.';
  for (const prefix of ['', 'long transcript prose '.repeat(4000)+'\n']) {
   const transcript = prefix + text;
   const ranges = prefix ? await window.codeRangesAsync(transcript, response, true) : window.codeRanges(transcript, true);
   assert(ranges !== null, 'worker failed');
   const marker = transcript.lastIndexOf('[Using tool:');
   assert(!window.isInsideCodeRanges(ranges, marker), 'tool marker classified as code');
   const literal = transcript.indexOf(tick+'!'+tick);
   assert(window.isInsideCodeRanges(ranges, literal+1), 'prose inline code lost');
   await window.renderStreamingContent(response, transcript, true);
   const cards = response.querySelectorAll('.stream-tool');
   assert(cards.length === 2, 'expected two tool cards, got '+cards.length);
   assert(cards[1].textContent.includes('5 files changed'), 'result not linked to second call');
   assert(!response.textContent.includes('[Using tool:'), 'raw tool marker leaked');
  }
  const fence = tick.repeat(3);
  await window.renderStreamingContent(response, fence+'text\n'+text+'\n'+fence, false);
  assert(!response.querySelector('.stream-tool'), 'fenced example became a tool call');

 }
 return {};
`)
}

func TestBrowserFunctional_ToolMetadataAndPartialOutputStayLiteral(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const tick = String.fromCharCode(96), response = stream;
 for (const prefix of ['', 'long transcript prose '.repeat(4000)+'\n']) {
  const command = '$ echo '+tick+'<thinking>'+tick;
  const call = '[Using tool: bash | '+command+']\n';
  const complete = call+'[Tool bash done]\nok\n[/Tool]\nFinal answer.';
  await window.renderStreamingContent(response, prefix+complete, true);
  assert(response.querySelectorAll('.stream-tool').length === 1, 'command alias broke tool card');
  assert(response.querySelector('.stream-tool').textContent.includes(command), 'command preview changed');
  assert(!response.querySelector('.stream-thinking'), 'command alias became thinking');
  const output = fence+'text\n[Using tool: read_file]\n[Thinking]\n<thinking>\n[Compaction started]\n'+fence;
  const partial = call+'[Tool bash done]\n'+output;
  for (const ending of ['', '\n[/To', '\n[/Tool]\nFinal answer.']) {
   await window.renderStreamingContent(response, prefix+partial+ending, true);
   assert(response.querySelectorAll('.stream-tool').length === 1, 'literal output created another tool card');
   assert(!response.querySelector('.stream-thinking'), 'literal output became thinking');
   assert(!response.querySelector('[data-compaction-state]'), 'literal output became compaction');
   if (ending.includes('Final answer.')) assert(response.querySelector('.stream-tool').textContent.includes(output), 'completed tool output changed');
  }
 }
 const quoted = fence+'text\n[Tool bash done]\n[Using tool: read_file]\n'+fence;
 await window.renderStreamingContent(response, quoted, false);
 assert(!response.querySelector('.stream-tool'), 'quoted unfinished output became a tool');
 return {};
 `)
}

func TestBrowserFunctional_PendingToolDetectionPreservesLaterThinking(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const tick = String.fromCharCode(96);
 const cases = [
  {text:'[Using tool: bash]\n[Tool bash done]\noutput[/Tool]\n<thinking>Real reasoning.</thinking>\nFinal answer.', tools:1},
  {text:'Example '+tick+'literal\n[Tool bash done]\ninline'+tick+'\n[Thinking]\nReal reasoning.\n[/Thinking]\nFinal answer.', tools:0},
  {text:'Example '+tick.repeat(2)+'literal\n[Tool bash done]\ninline'+tick.repeat(2)+'\n'+fence+'text\n[Using tool: read_file]\n'+fence+'\n<thinking>Real reasoning.</thinking>\nFinal answer.', tools:0}
 ];
 for (const sample of cases) {
  for (const prefix of ['', 'long transcript prose '.repeat(4000)+'\n']) {
   for (const newline of ['\n','\r\n']) {
    await window.renderStreamingContent(stream, (prefix+sample.text).replace(/\n/g,newline), true);
    assert(stream.querySelectorAll('.stream-tool').length === sample.tools, 'incorrect tool count');
    assert(stream.querySelectorAll('.stream-thinking').length === 1, 'later thinking was swallowed');
    assert(stream.querySelector('.stream-thinking').textContent.includes('Real reasoning.'), 'reasoning changed');
    assert(stream.textContent.includes('Final answer.'), 'final answer lost');
    assert(!stream.textContent.includes('<thinking>'), 'raw thinking alias leaked');
   }
  }
 }
 return {};
 `)
}

func TestBrowserFunctional_PartialToolQuotesCannotHideRealCalls(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const tick=String.fromCharCode(96);
 const text='Narration '+tick+'unfinished\n[Using tool: bash]\n[Tool bash done]\noutput closes '+tick+'\n[Using tool: read_file]\n';
 const example='Example '+tick+'literal\n[Tool bash done]\ninline'+tick+'\n';
 for (const prefix of ['', example, example.repeat(3), 'long transcript prose '.repeat(4000)+'\n'+example]) {
  for (const ending of ['', '\n[/Tool]\nFinished.']) {
   await window.renderStreamingContent(stream,prefix+text+ending,true);
   const cards=stream.querySelectorAll('.stream-tool');
   if (prefix.includes(example)) assert(Array.from(stream.querySelectorAll('code')).some(el=>el.textContent.includes('[Tool bash done]')), 'quoted example was not preserved as code');
   assert(cards.length===1 && cards[0].textContent.includes('Bash'), 'real Bash call was hidden or output became another call: '+JSON.stringify({prefix:prefix.slice(-80),ending,cards:Array.from(cards).map(c=>c.textContent)}));
  }
 }
 return {};
 `)
}

func TestBrowserPerformance_QuotedToolExamplesUseBoundedScans(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const tick=String.fromCharCode(96);
 const originalLines=window.markdownLineRanges;
 let scanned=0;
 window.markdownLineRanges=function(text) {scanned+=text.length;return originalLines(text);};
 const block='Example '+tick+'literal\n[Tool bash done]\ninline'+tick+'\n';
 for(const count of [100,500,1500,5000]) {
  const text=block.repeat(count);
  scanned=0;
  const ranges=window.codeRanges(text,true);
  assert(ranges.length===count, 'quoted examples lost');
  assert(scanned<=2*text.length, 'parser repeatedly rescanned remaining examples');
 }
 return {};
 `)
}

func TestBrowserFunctional_ShellAndLegacyBashUseInputRows(t *testing.T) {
	runIncrementalChatBrowser(t, `
 for (const sample of [
  {name:'shell', result:'shell', label:'Shell'},
  {name:'Shell', result:'shell', label:'Shell'},
  {name:'bash', result:'bash', label:'Bash'},
  {name:'Bash', result:'bash', label:'Bash'}
 ]) {
  const call = '[Using tool: '+sample.name+' | $ printf hello]\n';
  for (const suffix of ['', '[Tool '+sample.result+' done]\nhello\n[/Tool]\n']) {
   await window.renderStreamingContent(stream, call+suffix, false);
   const cards = stream.querySelectorAll('.stream-tool');
   assert(cards.length === 1, 'expected one tool card for '+sample.name);
   const card = cards[0];
   assert(card.querySelector('.tool-name-text').textContent === sample.label, 'incorrect display name');
   assert(!card.querySelector('.tool-name-secondary'), 'command leaked into header');
   const input = card.querySelector('[data-tool-row="in"] > pre');
   assert(input && input.textContent === '$ printf hello', 'command missing from IN row for '+sample.name);
   if (suffix) {
    const output = card.querySelector('[data-tool-row="out"] > pre');
    assert(output && output.textContent === 'hello', 'result missing from OUT row');
    assert(!card.querySelector('.tool-spinner'), 'completed tool still pending');
   }
  }
 }
 return {};
 `)
}
