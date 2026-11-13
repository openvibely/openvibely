#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
@interface Probe:NSObject<WKURLSchemeHandler,WKScriptMessageHandler>@end
@implementation Probe
-(void)userContentController:(WKUserContentController*)c didReceiveScriptMessage:(WKScriptMessage*)m { NSLog(@"REPORT %@",m.body); }
-(void)webView:(WKWebView*)w stopURLSchemeTask:(id<WKURLSchemeTask>)t {}
-(void)webView:(WKWebView*)w startURLSchemeTask:(id<WKURLSchemeTask>)t {
 NSURL *url=t.request.URL; NSLog(@"GET %@",url.path);NSString *body,*type=@"text/html";NSMutableDictionary *headers=[NSMutableDictionary new];
 if([url.path isEqual:@"/"]) body=@"<script>addEventListener('message',e=>webkit.messageHandlers.report.postMessage(e.data))</script><iframe sandbox='allow-scripts allow-forms' src='/plugin-assets/test/index.html'></iframe>";
 else if([url.path hasSuffix:@".js"]) {type=@"text/javascript";body=@"parent.postMessage('SCRIPT LOADED','*')";}
 else {body=@"<script>addEventListener('error',e=>parent.postMessage('ERROR '+(e.message||e.target.src),'*'),true);addEventListener('securitypolicyviolation',e=>parent.postMessage('CSP '+e.violatedDirective+' '+e.blockedURI,'*'));parent.postMessage('INLINE LOADED','*')</script><script src='app.js'></script>";headers[@"Content-Security-Policy"]=[[NSProcessInfo processInfo] arguments][1];}
 headers[@"Content-Type"]=type;headers[@"Access-Control-Allow-Origin"]=@"*";
 [t didReceiveResponse:[[NSHTTPURLResponse alloc]initWithURL:url statusCode:200 HTTPVersion:@"HTTP/1.1" headerFields:headers]];[t didReceiveData:[body dataUsingEncoding:NSUTF8StringEncoding]];[t didFinish];
}
@end
int main(){@autoreleasepool{[NSApplication sharedApplication];Probe *p=[Probe new];WKWebViewConfiguration *cfg=[WKWebViewConfiguration new];[cfg setURLSchemeHandler:p forURLScheme:@"wails"];[cfg.userContentController addScriptMessageHandler:p name:@"report"];WKWebView *v=[[WKWebView alloc]initWithFrame:NSMakeRect(0,0,500,500) configuration:cfg];[v loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:@"wails://localhost/"]]];dispatch_after(dispatch_time(DISPATCH_TIME_NOW,8*NSEC_PER_SEC),dispatch_get_main_queue(),^{exit(0);});[NSApp run];}}
