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
