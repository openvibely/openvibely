package layout

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestTaskCardActionSendsWorkspaceAndOwner(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	var out strings.Builder
	if err := TaskCardActionsScript().Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	script := out.String()
	start := strings.Index(script, "function runTaskCardAction(button)")
	end := strings.Index(script[start:], "\n\t\t\t}") + start + len("\n\t\t\t}")
	script = script[start:end]
	js := `const assert = require('node:assert/strict');
let scope = '__all_projects__', request;
global.window = {htmx:{}};
global.document = {getElementById: id => id === 'project-selector' ? {value:scope} : null};
global.CSS = {escape: x=>x};
global.htmx = {ajax:(method,path,options)=>{request={method,path,options}}};
function setTaskCardActionBusy(){}
` + script + `
for (const workspace of ['__all_projects__', '__repo_group', 'local']) {
 scope=workspace;
 for (const endpoint of ['merge','pull-request']) {
 window._taskCardActionRequest=null;
 runTaskCardAction({dataset:{taskId:'task',projectId:'owner',mergeType:'merge',mergeEndpoint:endpoint},hasAttribute:()=>true,closest:()=>null});
 assert.equal(new URL(request.path,'http://localhost').searchParams.get('project_id'), workspace);
 assert.equal(request.options.values.project_id,'owner');
 }
}
`
	if result, err := exec.Command(node, "-e", js).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, result)
	}
}
