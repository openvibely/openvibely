package handler

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeNestedScrollWaitsForMovement(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("requires node")
	}
	// Exercise the fixture's actual helper with native scrolling applied after
	// event dispatch. A virtual clock makes this deterministic and instant.
	script := `
const fs = require('fs');
const assert = require('assert');
const source = fs.readFileSync('testdata/native_thread_scroll.js', 'utf8');
const start = source.indexOf('    async function wheel(delta) {');
const end = source.indexOf('    await wheel({dy: 40});', start);
assert(start >= 0 && end > start);
const helper = source.slice(start, end);
(async () => {
 for (const delta of [{dy:40}, {dx:40,dy:0}]) {
  let clock = 0, pending;
  const listeners = new Set();
  const tool = {scrollTop:0, scrollLeft:0,
   getBoundingClientRect:()=>({left:20,top:120}),
   addEventListener:(_,fn)=>listeners.add(fn),
   removeEventListener:(_,fn)=>listeners.delete(fn)};
  const m = {}, bounds = {left:0,top:0};
  const advance = ms => { clock += ms; if(clock >= 200 && pending) {pending(); pending=null;} };
  const wait = async ms => advance(ms);
  const until = async (predicate,label) => {
   while(!predicate()) {
    if(clock >= 1000) throw Error('Timeout: '+label);
    advance(25);
   }
  };
  const stableLayout = async () => {};
  const nativeInput = {postMessage:async data => {
   for(const fn of [...listeners]) fn({isTrusted:true,deltaY:data.dy||0,deltaX:data.dx||0,defaultPrevented:false});
   for(const fn of [...listeners]) fn({isTrusted:true,deltaY:0,deltaX:0,defaultPrevented:false});
   pending = () => {tool[delta.dx?'scrollLeft':'scrollTop'] += 40;};
  }};
  const wheel = eval('('+helper.trim()+')');
  await wheel(delta);
  assert.equal(tool[delta.dx?'scrollLeft':'scrollTop'],40,'helper returned before native scroll took effect');
  assert.equal(listeners.size,0,'wheel listener leaked');
 }
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	require.NoError(t, err, "%s", output)
}
